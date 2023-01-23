package moodletokensmanager

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aDeepRecession/moodle-scrapper/pkg/ssoCredentials"
)

type cookieRequestManager struct{
	client *http.Client
    credentials ssocredentials.SsoCredentialsManager
    log *log.Logger
}

func newCookieRequestManager() (cookieRequestManager, error) {

	cookiejar, err := cookiejar.New(nil)
    if err != nil {
        return cookieRequestManager{}, err
	}
	client := &http.Client{
        Timeout: 5 * time.Second,
        Jar: cookiejar,
    }

    credentials := ssocredentials.NewCredentialsManager()
    logger := log.New(os.Stdout, log.Prefix(), log.Flags())

	return cookieRequestManager{client, credentials, logger}, nil
}

func (reqManager *cookieRequestManager) requestNewTokens() (MoodleCookies, error) {

    reqManager.log.Println("getting sso url...")
    ssoURL, err := reqManager.getSsoURL()
    if err != nil {
        return MoodleCookies{}, fmt.Errorf("failed to get new tokens")
    }

    reqManager.log.Println("logging in sso...")
    code, state, err := reqManager.loginInSSO(ssoURL)
    if err != nil {
        return MoodleCookies{}, fmt.Errorf("failed to get new tokens")
    }


    reqManager.log.Println("getting new tokens...")
    tokens, err := reqManager.getMoodleTokens(code, state)
    if err != nil {
        return MoodleCookies{}, fmt.Errorf("failed to get new tokens")
    }

    return tokens, nil
}

func (reqManager *cookieRequestManager) getMoodleTokens(code, state string) (MoodleCookies, error) {

    response, err := reqManager.sendMoodleCookieRequests(code, state)
    if err != nil {
        return MoodleCookies{}, fmt.Errorf("failed to get tokens from moodle: %v", err)
    }

    tokens, err := reqManager.extractTokens(response)
    if err != nil {
        return MoodleCookies{}, err
    }

    reqManager.client.CheckRedirect = nil

    return tokens, nil
}


func restrictRedirect(req *http.Request, mix []*http.Request) error {
    return http.ErrUseLastResponse
}  


func (reqManager *cookieRequestManager) sendMoodleCookieRequests(code, state string) (_ *http.Response, err error) {
    defer func() {
        if err != nil {
            err = fmt.Errorf("failed to send a series of requests to moodle: %v", err)
        }
    }()

    moodleData := url.Values{
        "code": {code},
        "state": {state},
    }
    moodlePostRequest, err := http.NewRequest(
        http.MethodPost,
        "https://moodle.innopolis.university:443/admin/oauth2callback.php",
        strings.NewReader(moodleData.Encode()),
    )
    if err != nil {
        return nil, err
    }
    moodlePostRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    reqManager.client.CheckRedirect = restrictRedirect

    moodleRes, err := reqManager.client.Do(moodlePostRequest)
    if err != nil {
        return nil, err
    }
    defer moodleRes.Body.Close()

    launchURL, err := moodleRes.Location()
    if err != nil {
        return nil, err
    }
    launchReq, err := http.NewRequest(http.MethodGet, launchURL.String(), nil)
    if err != nil {
        return nil, err
    }
    launchRes, err := reqManager.client.Do(launchReq)
    if err != nil {
        return nil, err
    }
    defer launchRes.Body.Close()

    launchReq2, err := http.NewRequest(http.MethodGet, launchRes.Header.Get("Location"), nil)
    if err != nil {
        return nil, err
    }
    launchRes2, err := reqManager.client.Do(launchReq2)
    if err != nil {
        return nil, err
    }
    defer launchRes2.Body.Close()

    return launchRes2, nil
}


func (reqManager *cookieRequestManager) extractTokens(response *http.Response) (MoodleCookies, error) {

    encryptedTokens := strings.ReplaceAll(response.Header.Get("Location"), "moodlemobile://token=", "")

    byteTokens := make([]byte, 126)
    _, err := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encryptedTokens)).Read(byteTokens)
    if err != nil {
        return MoodleCookies{}, fmt.Errorf("failed to extract tokens: %v", err)
    }

    tokens := strings.Split(string(byteTokens), ":::")

    return MoodleCookies{tokens[0], tokens[1], tokens[2]}, nil
}


func (reqManager *cookieRequestManager) loginInSSO(ssoURL string) (code string, state string, err error) {

    credentials, err := reqManager.credentials.GetLoginCredentials()
    if err != nil {
        return "", "", fmt.Errorf("failed to login in SSO: %v", err)
    }

    ssoData := url.Values{
        "UserName": {credentials.Login},
        "Password": {credentials.Password},
        "Kmsi": {"true"},
        "AuthMethod": {"FormsAuthentication"},
    }
    ssoResponse, err := reqManager.sendSSOPostRequest(ssoURL, ssoData)
    if err != nil {
        return "", "", fmt.Errorf("failed to login in SSO: %v", err)
    }

    codeRegex := regexp.MustCompile(`(?:name="code" value=")(.+?)" />`)
    code = codeRegex.FindStringSubmatch(ssoResponse)[1]

    stateRegex := regexp.MustCompile(`(?:name="state" value=")(.+?)" />`)
    state = stateRegex.FindStringSubmatch(ssoResponse)[1]

    return code, state, err
}


func (reqManager *cookieRequestManager) sendSSOPostRequest(ssoURL string, ssoData url.Values) (string, error) {
    ssoReq, err := http.NewRequest(http.MethodPost, ssoURL, strings.NewReader(ssoData.Encode()))
    if err != nil {
        return "", fmt.Errorf("failed to send SSO POST request: %v", err)
    }
    ssoRes, err := reqManager.client.Do(ssoReq)
    if err != nil {
        return "", fmt.Errorf("failed to send SSO POST request: %v", err)
    }
    defer ssoRes.Body.Close()

    ssoResBody, err := io.ReadAll(ssoRes.Body)
    if err != nil {
        return "", fmt.Errorf("failed to send SSO POST request: %v", err)
    }

    return string(ssoResBody), nil
}

func (reqManager *cookieRequestManager) getSsoURL() (string, error) {

    loginUrl, err := reqManager.getMoodleLoginButtonURL()
    if err != nil {
        return "", fmt.Errorf("failed to get SSO URL: %v", err)
    }

    req, err := http.NewRequest(http.MethodGet, loginUrl, nil)
    if err != nil {
        return "", fmt.Errorf("failed to get SSO URL: %v", err)
    }
    
    loginButtonResponse, err := reqManager.client.Do(req)
    if err != nil {
        return "", fmt.Errorf("failed to get SSO URL: %v", err)
    }
    defer loginButtonResponse.Body.Close()
    

    ssoURL := loginButtonResponse.Request.URL.String()

    return ssoURL, nil
}


func (reqManager *cookieRequestManager) getMoodleLoginButtonURL() (string, error)  {

    req, err := http.NewRequest(
        http.MethodGet,
        "https://moodle.innopolis.university/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=1",
        nil,
    )
    if err != nil {
        return "", fmt.Errorf("failed to get moodle button url: %v", err)
    }

    loginPage, err := reqManager.client.Do(req)
    if err != nil {
        return "", fmt.Errorf("failed to get moodle button url: %v", err)
    }
    defer loginPage.Body.Close()

    res, err := io.ReadAll(loginPage.Body)
    if err != nil {
        return "", fmt.Errorf("failed to get moodle button url: %v", err)
    }

    regexLoginUrlPattern := regexp.MustCompile(
        `https://moodle\.innopolis\.university/auth/oauth2/login\.php\?id=1&amp;wantsurl=https%3A%2F%2Fmoodle\.innopolis\.university%2Fadmin%2Ftool%2Fmobile%2Flaunch\.php%3Fservice%3Dmoodle_mobile_app%26passport%3D1&amp;sesskey=[^"]+`,
    )

    loginUrl := string(regexLoginUrlPattern.Find(res))
    if loginUrl == "" {
        return "", fmt.Errorf("failed to get moodle button url: %v", err)
    }

    loginUrl = strings.ReplaceAll(loginUrl, "&amp;", "&")

    return loginUrl, nil
}


