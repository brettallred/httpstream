package twitterstream

import (
    "bufio"
    "bytes"
    "container/vector"
    "crypto/hmac"
    "encoding/base64"
    "fmt"
    "http"
    "httplib"
    "json"
    "os"
    "rand"
    "sort"
    "strings"
    "strconv"
    "time"
)

var requestTokenUrl, _ = http.ParseURL("https://api.twitter.com/oauth/request_token")
var accessTokenUrl, _ = http.ParseURL("https://api.twitter.com/oauth/access_token")
var authorizeUrl, _ = http.ParseURL("https://api.twitter.com/oauth/authorize")

type OAuthClient struct {
    ConsumerKey    string
    ConsumerSecret string
    Stream         chan Tweet
    //the ccurrent connection to the stream client
    streamClient *oauthStreamClient
}

type oauthStreamClient struct {
    httpClient *httplib.HttpRequestBuilder
    headers    map[string]string
    params     map[string]string
    url        string
    closed     bool
    stream     chan Tweet
}

func NewOAuthClient(consumerKey string, consumerSecret string) *OAuthClient {
    return &OAuthClient{
        ConsumerKey:    consumerKey,
        ConsumerSecret: consumerSecret,
        Stream:         make(chan Tweet),
    }
}

type RequestToken struct {
    OAuthTokenSecret       string
    OAuthToken             string
    OAuthCallbackConfirmed bool
}

type AccessToken struct {
    OAuthToken       string
    OAuthTokenSecret string
    UserId           string
    ScreenName       string
}

func getNonce(n int) string {
    var alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
    buf := make([]byte, n)
    for i := 0; i < n; i++ {
        buf[i] = alphabet[rand.Intn(len(alphabet))]
    }
    return string(buf)
}

func signatureBase(httpMethod string, base_uri string, params map[string]string) string {
    var buf bytes.Buffer

    buf.WriteString(httpMethod)
    buf.WriteString("&")
    buf.WriteString(URLEscape(base_uri))
    buf.WriteString("&")

    var keys vector.StringVector
    for k, _ := range params {
        keys.Push(k)
    }

    sort.SortStrings(keys)
    for i, k := range keys {
        v := params[k]
        buf.WriteString(URLEscape(k))
        buf.WriteString("%3D")
        buf.WriteString(URLEscape(v))
        //don't include the dangling %26
        if i < len(params)-1 {
            buf.WriteString("%26")
        }
        i++
    }
    return buf.String()
}

func signRequest(base string, consumerSecret string, tokenSecret string) string {
    signingKey := URLEscape(consumerSecret) + "&"
    if tokenSecret != "" {
        signingKey += URLEscape(tokenSecret)
    }
    hash := hmac.NewSHA1([]byte(signingKey))
    hash.Write([]byte(base))
    sum := hash.Sum()
    bb := new(bytes.Buffer)
    encoder := base64.NewEncoder(base64.StdEncoding, bb)
    encoder.Write(sum)
    encoder.Close()
    return bb.String()
}

func (o *OAuthClient) GetRequestToken(callback string) *RequestToken {
    nonce := getNonce(40)
    params := map[string]string{
        "oauth_nonce":            nonce,
        "oauth_callback":         URLEscape(callback),
        "oauth_signature_method": "HMAC-SHA1",
        "oauth_timestamp":        strconv.Itoa64(time.Seconds()),
        "oauth_consumer_key":     o.ConsumerKey,
        "oauth_version":          "1.0",
    }

    base := signatureBase("POST", requestTokenUrl.Raw, params)
    signature := signRequest(base, o.ConsumerSecret, "")
    params["oauth_signature"] = URLEscape(signature)

    authBuf := bytes.NewBufferString("OAuth ")
    i := 0
    for k, v := range params {
        authBuf.WriteString(fmt.Sprintf("%s=%q", k, v))
        if i < len(params)-1 {
            authBuf.WriteString(", ")
        }
        i++
    }
    request := httplib.Post(requestTokenUrl.Raw)
    request.Header("Authorization", authBuf.String())
    request.Body("")
    resp, err := request.AsString()
    tokens, err := http.ParseQuery(resp)
    if err != nil {
        println(err.String())
    }

    confirmed, _ := strconv.Atob(tokens["oauth_callback_confirmed"][0])
    rt := RequestToken{
        OAuthTokenSecret:       tokens["oauth_token_secret"][0],
        OAuthToken:             tokens["oauth_token"][0],
        OAuthCallbackConfirmed: confirmed,
    }
    return &rt
}

func (rt *RequestToken) AuthorizeUrl() string {
    return fmt.Sprintf("%s?oauth_token=%s", authorizeUrl.Raw, rt.OAuthToken)
}

func (o *OAuthClient) GetAccessToken(requestToken *RequestToken, OAuthVerifier string) (*AccessToken, os.Error) {
    if requestToken == nil || requestToken.OAuthToken == "" || requestToken.OAuthTokenSecret == "" {
        return nil, os.NewError("Invalid Request token")
    }
    
    nonce := getNonce(40)
    params := map[string]string{
        "oauth_nonce":            nonce,
        "oauth_token":            requestToken.OAuthToken,
        "oauth_verifier":         OAuthVerifier,
        "oauth_signature_method": "HMAC-SHA1",
        "oauth_timestamp":        strconv.Itoa64(time.Seconds()),
        "oauth_consumer_key":     o.ConsumerKey,
        "oauth_version":          "1.0",
    }

    base := signatureBase("POST", requestTokenUrl.Raw, params)
    signature := signRequest(base, o.ConsumerSecret, requestToken.OAuthTokenSecret)
    params["oauth_signature"] = URLEscape(signature)

    authBuf := bytes.NewBufferString("OAuth ")
    i := 0
    for k, v := range params {
        authBuf.WriteString(fmt.Sprintf("%s=%q", k, v))
        if i < len(params)-1 {
            authBuf.WriteString(", ")
        }
        i++
    }
    request := httplib.Post(accessTokenUrl.Raw)
    request.Header("Authorization", authBuf.String())
    request.Body("")
    resp, err := request.AsString()
    tokens, err := http.ParseQuery(resp)
    if err != nil {
        return nil, err
    }

    at := AccessToken{
        OAuthTokenSecret: tokens["oauth_token_secret"][0],
        OAuthToken:       tokens["oauth_token"][0],
        UserId:           tokens["user_id"][0],
        ScreenName:       tokens["screen_name"][0],
    }
    return &at, nil

}

func (c *oauthStreamClient) connect() (*http.Response, os.Error) {
    c.httpClient = httplib.Post(c.url)
    for k, v := range c.headers {
        c.httpClient.Header(k, v)
    }

    var body bytes.Buffer
    for k, v := range c.params {
        body.WriteString(URLEscape(k))
        body.WriteString("=")
        body.WriteString(URLEscape(v))
    }
    c.httpClient.Body(body.String())

    //make the new connection
    return c.httpClient.AsResponse()
}

func (c *oauthStreamClient) readStream(resp *http.Response) {
    var reader *bufio.Reader
    reader = bufio.NewReader(resp.Body)
    for {
        //we've been closed
        if c.closed {
            c.httpClient.Close()
            break
        }

        line, err := reader.ReadBytes('\n')
        if err != nil {
            if c.closed {
                continue
            }
            resp, err := c.connect()
            if err != nil {
                println(err.String())
                time.Sleep(retryTimeout)
                continue
            }

            if resp.StatusCode != 200 {
                continue
            }

            reader = bufio.NewReader(resp.Body)
            continue
        }
        line = bytes.TrimSpace(line)

        if len(line) == 0 {
            continue
        }

        var message SiteStreamMessage
        json.Unmarshal(line, &message)
        if message.Message.Id != 0 {
            c.stream <- message.Message
        }
    }
}

func (c *oauthStreamClient) close() {
    c.closed = true
    c.httpClient.Close()

}

func (o *OAuthClient) connect(url string, OAuthToken string, OAuthTokenSecret string, form map[string]string) os.Error {
    nonce := getNonce(40)

    params := map[string]string{
        "oauth_nonce":            nonce,
        "oauth_token":            OAuthToken,
        "oauth_signature_method": "HMAC-SHA1",
        "oauth_timestamp":        strconv.Itoa64(time.Seconds()),
        "oauth_consumer_key":     o.ConsumerKey,
        "oauth_version":          "1.0",
    }

    //add the form to the params
    for k, v := range form {
        params[URLEscape(k)] = URLEscape(v)
    }

    base := signatureBase("POST", url, params)
    signature := signRequest(base, o.ConsumerSecret, OAuthTokenSecret)

    params["oauth_signature"] = URLEscape(signature)

    authBuf := bytes.NewBufferString("OAuth ")
    for k, v := range params {
        if strings.HasPrefix(k, "oauth_") {
            authBuf.WriteString(fmt.Sprintf("%s=%q, ", k, v))
        }
    }

    authBufString := authBuf.String()
    if len(authBufString) > 0 {
        authBufString = authBufString[0 : len(authBufString)-2]
    }

    streamClient := new(oauthStreamClient)
    streamClient.url = url
    streamClient.params = form
    streamClient.headers = map[string]string{
        "Authorization": authBufString,
        "Content-Type":  "application/x-www-form-urlencoded",
    }

    //close the existing connection
    if o.streamClient != nil {
        o.streamClient.close()
    }

    resp, err := streamClient.connect()
    if err != nil {
        return err
    }

    //TODO: handle non-streaming methods here
    go streamClient.readStream(resp)

    o.streamClient = streamClient
    streamClient.stream = o.Stream

    return nil
}

func (o *OAuthClient) SiteStream(OAuthToken string, OAuthTokenSecret string, ids []int64) os.Error {
    //build the follow string
    var buf bytes.Buffer
    for i, id := range ids {
        buf.WriteString(strconv.Itoa64(id))
        if i != len(ids)-1 {
            buf.WriteString(",")
        }
    }
    params := map[string]string{"follow": buf.String()}
    return o.connect(siteStreamUrl.Raw, OAuthToken, OAuthTokenSecret, params)
}


// Close the client
func (o *OAuthClient) Close() {
    //has it already been closed?
    if o.streamClient.closed {
        return
    }
    o.streamClient.close()
}
