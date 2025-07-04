/*
Okta Admin Management

Allows customers to easily access the Okta Management APIs

Copyright 2018 - Present Okta, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

API version: 2024.06.1
Contact: devex-public@okta.com
*/

package okta

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cenkalti/backoff/v4"
	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwk"
	goCache "github.com/patrickmn/go-cache"
	"golang.org/x/oauth2"
)

var (
	jsonCheck = regexp.MustCompile(`(?i:(?:application|text)/(?:vnd\.[^;]+\+)?json)`)
	xmlCheck  = regexp.MustCompile(`(?i:(?:application|text)/xml)`)
)

const (
	VERSION                   = "5.0.0"
	AccessTokenCacheKey       = "OKTA_ACCESS_TOKEN"
	DpopAccessTokenNonce      = "DPOP_OKTA_ACCESS_TOKEN_NONCE"
	DpopAccessTokenPrivateKey = "DPOP_OKTA_ACCESS_TOKEN_PRIVATE_KEY"
)

type RateLimit struct {
	Limit     int
	Remaining int
	Reset     int64
}

// APIClient manages communication with the Okta Admin Management API v2024.06.1
// In most cases there should be only one, shared, APIClient.
type APIClient struct {
	cfg           *Configuration
	common        service // Reuse a single struct instead of allocating one for each service on the heap.
	cache         Cache
	tokenCache    *goCache.Cache
	freshcache    bool
	rateLimit     *RateLimit
	rateLimitLock sync.Mutex

	// API Services

	AgentPoolsAPI AgentPoolsAPI

	ApiServiceIntegrationsAPI ApiServiceIntegrationsAPI

	ApiTokenAPI ApiTokenAPI

	ApplicationAPI ApplicationAPI

	ApplicationConnectionsAPI ApplicationConnectionsAPI

	ApplicationCredentialsAPI ApplicationCredentialsAPI

	ApplicationFeaturesAPI ApplicationFeaturesAPI

	ApplicationGrantsAPI ApplicationGrantsAPI

	ApplicationGroupsAPI ApplicationGroupsAPI

	ApplicationLogosAPI ApplicationLogosAPI

	ApplicationPoliciesAPI ApplicationPoliciesAPI

	ApplicationSSOAPI ApplicationSSOAPI

	ApplicationTokensAPI ApplicationTokensAPI

	ApplicationUsersAPI ApplicationUsersAPI

	AttackProtectionAPI AttackProtectionAPI

	AuthenticatorAPI AuthenticatorAPI

	AuthorizationServerAPI AuthorizationServerAPI

	AuthorizationServerAssocAPI AuthorizationServerAssocAPI

	AuthorizationServerClaimsAPI AuthorizationServerClaimsAPI

	AuthorizationServerClientsAPI AuthorizationServerClientsAPI

	AuthorizationServerKeysAPI AuthorizationServerKeysAPI

	AuthorizationServerPoliciesAPI AuthorizationServerPoliciesAPI

	AuthorizationServerRulesAPI AuthorizationServerRulesAPI

	AuthorizationServerScopesAPI AuthorizationServerScopesAPI

	BehaviorAPI BehaviorAPI

	BrandsAPI BrandsAPI

	CAPTCHAAPI CAPTCHAAPI

	CustomDomainAPI CustomDomainAPI

	CustomPagesAPI CustomPagesAPI

	CustomTemplatesAPI CustomTemplatesAPI

	DeviceAPI DeviceAPI

	DeviceAssuranceAPI DeviceAssuranceAPI

	DirectoriesIntegrationAPI DirectoriesIntegrationAPI

	EmailDomainAPI EmailDomainAPI

	EmailServerAPI EmailServerAPI

	EventHookAPI EventHookAPI

	FeatureAPI FeatureAPI

	GroupAPI GroupAPI

	GroupOwnerAPI GroupOwnerAPI

	HookKeyAPI HookKeyAPI

	IdentityProviderAPI IdentityProviderAPI

	IdentitySourceAPI IdentitySourceAPI

	InlineHookAPI InlineHookAPI

	LinkedObjectAPI LinkedObjectAPI

	LogStreamAPI LogStreamAPI

	NetworkZoneAPI NetworkZoneAPI

	OktaApplicationSettingsAPI OktaApplicationSettingsAPI

	OrgSettingAPI OrgSettingAPI

	PolicyAPI PolicyAPI

	PrincipalRateLimitAPI PrincipalRateLimitAPI

	ProfileMappingAPI ProfileMappingAPI

	PushProviderAPI PushProviderAPI

	RateLimitSettingsAPI RateLimitSettingsAPI

	RealmAPI RealmAPI

	RealmAssignmentAPI RealmAssignmentAPI

	ResourceSetAPI ResourceSetAPI

	RiskEventAPI RiskEventAPI

	RiskProviderAPI RiskProviderAPI

	RoleAPI RoleAPI

	RoleAssignmentAPI RoleAssignmentAPI

	RoleTargetAPI RoleTargetAPI

	SSFReceiverAPI SSFReceiverAPI

	SSFSecurityEventTokenAPI SSFSecurityEventTokenAPI

	SSFTransmitterAPI SSFTransmitterAPI

	SchemaAPI SchemaAPI

	SessionAPI SessionAPI

	SubscriptionAPI SubscriptionAPI

	SystemLogAPI SystemLogAPI

	TemplateAPI TemplateAPI

	ThemesAPI ThemesAPI

	ThreatInsightAPI ThreatInsightAPI

	TrustedOriginAPI TrustedOriginAPI

	UISchemaAPI UISchemaAPI

	UserAPI UserAPI

	UserFactorAPI UserFactorAPI

	UserTypeAPI UserTypeAPI
}

type service struct {
	client *APIClient
}

type Authorization interface {
	Authorize(method, URL string) error
}

type SSWSAuth struct {
	token string
	req   *http.Request
}

func NewSSWSAuth(token string, req *http.Request) *SSWSAuth {
	return &SSWSAuth{token: token, req: req}
}

func (a *SSWSAuth) Authorize(method, URL string) error {
	a.req.Header.Add("Authorization", "SSWS "+a.token)
	return nil
}

type BearerAuth struct {
	token string
	req   *http.Request
}

func NewBearerAuth(token string, req *http.Request) *BearerAuth {
	return &BearerAuth{token: token, req: req}
}

func (a *BearerAuth) Authorize(method, URL string) error {
	a.req.Header.Add("Authorization", "Bearer "+a.token)
	return nil
}

type PrivateKeyAuth struct {
	tokenCache       *goCache.Cache
	httpClient       *http.Client
	privateKeySigner jose.Signer
	privateKey       string
	privateKeyId     string
	clientId         string
	orgURL           string
	userAgent        string
	scopes           []string
	maxRetries       int32
	maxBackoff       int64
	req              *http.Request
}

type PrivateKeyAuthConfig struct {
	TokenCache       *goCache.Cache
	HttpClient       *http.Client
	PrivateKeySigner jose.Signer
	PrivateKey       string
	PrivateKeyId     string
	ClientId         string
	OrgURL           string
	UserAgent        string
	Scopes           []string
	MaxRetries       int32
	MaxBackoff       int64
	Req              *http.Request
}

func NewPrivateKeyAuth(config PrivateKeyAuthConfig) *PrivateKeyAuth {
	return &PrivateKeyAuth{
		tokenCache:       config.TokenCache,
		httpClient:       config.HttpClient,
		privateKeySigner: config.PrivateKeySigner,
		privateKey:       config.PrivateKey,
		privateKeyId:     config.PrivateKeyId,
		clientId:         config.ClientId,
		orgURL:           config.OrgURL,
		userAgent:        config.UserAgent,
		scopes:           config.Scopes,
		maxRetries:       config.MaxRetries,
		maxBackoff:       config.MaxBackoff,
		req:              config.Req,
	}
}

func (a *PrivateKeyAuth) Authorize(method, URL string) error {
	accessToken, hasToken := a.tokenCache.Get(AccessTokenCacheKey)
	if hasToken && accessToken != "" {
		accessTokenWithTokenType := accessToken.(string)
		a.req.Header.Add("Authorization", accessTokenWithTokenType)
		nonce, hasNonce := a.tokenCache.Get(DpopAccessTokenNonce)
		if hasNonce && nonce != "" {
			privateKey, ok := a.tokenCache.Get(DpopAccessTokenPrivateKey)
			if ok && privateKey != nil {
				res := strings.Split(accessTokenWithTokenType, " ")
				if len(res) != 2 {
					return errors.New("Unidentified access token")
				}
				dpopJWT, err := generateDpopJWT(privateKey.(*rsa.PrivateKey), method, URL, nonce.(string), res[1])
				if err != nil {
					return err
				}
				a.req.Header.Set("Dpop", dpopJWT)
				a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
			} else {
				return errors.New("Using Dpop but signing key not found")
			}
		}
	} else {
		if a.privateKeySigner == nil {
			var err error
			a.privateKeySigner, err = createKeySigner(a.privateKey, a.privateKeyId)
			if err != nil {
				return err
			}
		}

		clientAssertion, err := createClientAssertion(a.orgURL, a.clientId, a.privateKeySigner)
		if err != nil {
			return err
		}

		accessToken, nonce, privateKey, err := getAccessTokenForPrivateKey(a.httpClient, a.orgURL, clientAssertion, a.userAgent, a.scopes, a.maxRetries, a.maxBackoff, a.clientId, a.privateKeySigner)
		if err != nil {
			return err
		}

		if accessToken == nil {
			return errors.New("Empty access token")
		}

		a.req.Header.Set("Authorization", fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken))
		if accessToken.TokenType == "DPoP" {
			dpopJWT, err := generateDpopJWT(privateKey, method, URL, nonce, accessToken.AccessToken)
			if err != nil {
				return err
			}
			a.req.Header.Set("Dpop", dpopJWT)
			a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
		}

		// Trim a couple of seconds off calculated expiry so cache expiry
		// occures before Okta server side expiry.
		expiration := accessToken.ExpiresIn - 2
		a.tokenCache.Set(AccessTokenCacheKey, fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken), time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenNonce, nonce, time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenPrivateKey, privateKey, time.Second*time.Duration(expiration))
	}
	return nil
}

type JWTAuth struct {
	tokenCache      *goCache.Cache
	httpClient      *http.Client
	orgURL          string
	userAgent       string
	scopes          []string
	clientAssertion string
	maxRetries      int32
	maxBackoff      int64
	req             *http.Request
}

type JWTAuthConfig struct {
	TokenCache      *goCache.Cache
	HttpClient      *http.Client
	OrgURL          string
	UserAgent       string
	Scopes          []string
	ClientAssertion string
	MaxRetries      int32
	MaxBackoff      int64
	Req             *http.Request
}

func NewJWTAuth(config JWTAuthConfig) *JWTAuth {
	return &JWTAuth{
		tokenCache:      config.TokenCache,
		httpClient:      config.HttpClient,
		orgURL:          config.OrgURL,
		userAgent:       config.UserAgent,
		scopes:          config.Scopes,
		clientAssertion: config.ClientAssertion,
		maxRetries:      config.MaxRetries,
		maxBackoff:      config.MaxBackoff,
		req:             config.Req,
	}
}

func (a *JWTAuth) Authorize(method, URL string) error {
	accessToken, hasToken := a.tokenCache.Get(AccessTokenCacheKey)
	if hasToken && accessToken != "" {
		accessTokenWithTokenType := accessToken.(string)
		a.req.Header.Add("Authorization", accessTokenWithTokenType)
		nonce, hasNonce := a.tokenCache.Get(DpopAccessTokenNonce)
		if hasNonce && nonce != "" {
			privateKey, ok := a.tokenCache.Get(DpopAccessTokenPrivateKey)
			if ok && privateKey != nil {
				res := strings.Split(accessTokenWithTokenType, " ")
				if len(res) != 2 {
					return errors.New("Unidentified access token")
				}
				dpopJWT, err := generateDpopJWT(privateKey.(*rsa.PrivateKey), method, URL, nonce.(string), res[1])
				if err != nil {
					return err
				}
				a.req.Header.Set("Dpop", dpopJWT)
				a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
			} else {
				return errors.New("Using Dpop but signing key not found")
			}
		}
	} else {
		accessToken, nonce, privateKey, err := getAccessTokenForPrivateKey(a.httpClient, a.orgURL, a.clientAssertion, a.userAgent, a.scopes, a.maxRetries, a.maxBackoff, "", nil)
		if err != nil {
			return err
		}

		if accessToken == nil {
			return errors.New("Empty access token")
		}

		a.req.Header.Set("Authorization", fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken))
		if accessToken.TokenType == "DPoP" {
			dpopJWT, err := generateDpopJWT(privateKey, method, URL, nonce, accessToken.AccessToken)
			if err != nil {
				return err
			}
			a.req.Header.Set("Dpop", dpopJWT)
			a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
		}

		// Trim a couple of seconds off calculated expiry so cache expiry
		// occures before Okta server side expiry.
		expiration := accessToken.ExpiresIn - 2
		a.tokenCache.Set(AccessTokenCacheKey, fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken), time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenNonce, nonce, time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenPrivateKey, privateKey, time.Second*time.Duration(expiration))
	}
	return nil
}

type JWKAuth struct {
	tokenCache       *goCache.Cache
	httpClient       *http.Client
	jwk              string
	encryptionType   string
	privateKeySigner jose.Signer
	privateKey       string
	privateKeyId     string
	clientId         string
	orgURL           string
	userAgent        string
	scopes           []string
	maxRetries       int32
	maxBackoff       int64
	req              *http.Request
}

type JWKAuthConfig struct {
	TokenCache       *goCache.Cache
	HttpClient       *http.Client
	JWK              string
	EncryptionType   string
	PrivateKeySigner jose.Signer
	PrivateKeyId     string
	ClientId         string
	OrgURL           string
	UserAgent        string
	Scopes           []string
	MaxRetries       int32
	MaxBackoff       int64
	Req              *http.Request
}

func NewJWKAuth(config JWKAuthConfig) *JWKAuth {
	return &JWKAuth{
		tokenCache:       config.TokenCache,
		httpClient:       config.HttpClient,
		jwk:              config.JWK,
		encryptionType:   config.EncryptionType,
		privateKeySigner: config.PrivateKeySigner,
		privateKeyId:     config.PrivateKeyId,
		clientId:         config.ClientId,
		orgURL:           config.OrgURL,
		userAgent:        config.UserAgent,
		scopes:           config.Scopes,
		maxRetries:       config.MaxRetries,
		maxBackoff:       config.MaxBackoff,
		req:              config.Req,
	}
}

func (a *JWKAuth) Authorize(method, URL string) error {
	accessToken, hasToken := a.tokenCache.Get(AccessTokenCacheKey)
	if hasToken && accessToken != "" {
		accessTokenWithTokenType := accessToken.(string)
		a.req.Header.Add("Authorization", accessTokenWithTokenType)
		nonce, hasNonce := a.tokenCache.Get(DpopAccessTokenNonce)
		if hasNonce && nonce != "" {
			privateKey, ok := a.tokenCache.Get(DpopAccessTokenPrivateKey)
			if ok && privateKey != nil {
				res := strings.Split(accessTokenWithTokenType, " ")
				if len(res) != 2 {
					return errors.New("Unidentified access token")
				}
				dpopJWT, err := generateDpopJWT(privateKey.(*rsa.PrivateKey), method, URL, nonce.(string), res[1])
				if err != nil {
					return err
				}
				a.req.Header.Set("Dpop", dpopJWT)
				a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
			} else {
				return errors.New("Using Dpop but signing key not found")
			}
		}
	} else {
		privateKey, err := convertJWKToPrivateKey(a.jwk, a.encryptionType)
		if err != nil {
			return err
		}
		if a.privateKeySigner == nil {
			var err error
			a.privateKeySigner, err = createKeySigner(privateKey, a.privateKeyId)
			if err != nil {
				return err
			}
		}

		clientAssertion, err := createClientAssertion(a.orgURL, a.clientId, a.privateKeySigner)
		if err != nil {
			return err
		}

		accessToken, nonce, dpopPrivateKey, err := getAccessTokenForPrivateKey(a.httpClient, a.orgURL, clientAssertion, a.userAgent, a.scopes, a.maxRetries, a.maxBackoff, "", nil)
		if err != nil {
			return err
		}

		if accessToken == nil {
			return errors.New("Empty access token")
		}

		a.req.Header.Set("Authorization", fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken))
		if accessToken.TokenType == "DPoP" {
			dpopJWT, err := generateDpopJWT(dpopPrivateKey, method, URL, nonce, accessToken.AccessToken)
			if err != nil {
				return err
			}
			a.req.Header.Set("Dpop", dpopJWT)
			a.req.Header.Set("x-okta-user-agent-extended", "isDPoP:true")
		}

		// Trim a couple of seconds off calculated expiry so cache expiry
		// occures before Okta server side expiry.
		expiration := accessToken.ExpiresIn - 2
		a.tokenCache.Set(AccessTokenCacheKey, fmt.Sprintf("%v %v", accessToken.TokenType, accessToken.AccessToken), time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenNonce, nonce, time.Second*time.Duration(expiration))
		a.tokenCache.Set(DpopAccessTokenPrivateKey, dpopPrivateKey, time.Second*time.Duration(expiration))
	}
	return nil
}

func convertJWKToPrivateKey(jwks, encryptionType string) (string, error) {
	set, err := jwk.Parse([]byte(jwks))
	if err != nil {
		return "", err
	}

	for i := range set.Keys() {
		key, ok := set.Key(i)
		if !ok {
			return "", fmt.Errorf("failed to get key at index %d", i)
		}
		var rawkey interface{} // This is the raw key, like *rsa.PrivateKey or *ecdsa.PrivateKey
		err := jwk.Export(key, &rawkey)
		if err != nil {
			return "", err
		}

		switch encryptionType {
		case "RSA":
			rsaPrivateKey, ok := rawkey.(*rsa.PrivateKey)
			if !ok {
				return "", fmt.Errorf("expected rsa key, got %T", rawkey)
			}
			return string(privateKeyToBytes(rsaPrivateKey)), nil
		default:
			return "", fmt.Errorf("unknown encryptionType %v", encryptionType)
		}
	}
	return "", fmt.Errorf("unknown encryptionType %v", encryptionType)
}

func createKeySigner(privateKey, privateKeyID string) (jose.Signer, error) {
	var signerOptions *jose.SignerOptions
	if privateKeyID != "" {
		signerOptions = (&jose.SignerOptions{}).WithHeader("kid", privateKeyID)
	}

	priv := []byte(strings.ReplaceAll(privateKey, `\n`, "\n"))

	privPem, _ := pem.Decode(priv)
	if privPem == nil {
		return nil, errors.New("invalid private key")
	}
	if privPem.Type == "RSA PRIVATE KEY" {
		parsedKey, err := x509.ParsePKCS1PrivateKey(privPem.Bytes)
		if err != nil {
			return nil, err
		}
		return jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: parsedKey}, signerOptions)
	}
	if privPem.Type == "PRIVATE KEY" {
		parsedKey, err := x509.ParsePKCS8PrivateKey(privPem.Bytes)
		if err != nil {
			return nil, err
		}
		var alg jose.SignatureAlgorithm
		switch parsedKey.(type) {
		case *rsa.PrivateKey:
			alg = jose.RS256
		case *ecdsa.PrivateKey:
			alg = jose.ES256 // TODO handle ES384 or ES512 ?
		default:
			// TODO are either of these also valid?
			// ed25519.PrivateKey:
			// *ecdh.PrivateKey
			return nil, fmt.Errorf("private key %q is unknown pkcs#8 format type", privPem.Type)
		}
		return jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: parsedKey}, signerOptions)
	}

	return nil, fmt.Errorf("private key %q is not pkcs#1 or pkcs#8 format", privPem.Type)
}

func createClientAssertion(orgURL, clientID string, privateKeySinger jose.Signer) (clientAssertion string, err error) {
	claims := ClientAssertionClaims{
		Subject:  clientID,
		IssuedAt: jwt.NewNumericDate(time.Now()),
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour * time.Duration(1))),
		Issuer:   clientID,
		Audience: orgURL + "/oauth2/v1/token",
		ID:       uuid.New().String(),
	}
	jwtBuilder := jwt.Signed(privateKeySinger).Claims(claims)
	return jwtBuilder.CompactSerialize()
}

func getAccessTokenForPrivateKey(httpClient *http.Client, orgURL, clientAssertion, userAgent string, scopes []string, maxRetries int32, maxBackoff int64, clientID string, signer jose.Signer) (*RequestAccessToken, string, *rsa.PrivateKey, error) {
	query := url.Values{}
	tokenRequestURL := orgURL + "/oauth2/v1/token"

	query.Add("grant_type", "client_credentials")
	query.Add("scope", strings.Join(scopes, " "))
	query.Add("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	query.Add("client_assertion", clientAssertion)

	tokenRequest, err := http.NewRequest("POST", tokenRequestURL, strings.NewReader(query.Encode()))
	if err != nil {
		return nil, "", nil, err
	}
	tokenRequest.Header.Add("Accept", "application/json")
	tokenRequest.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	tokenRequest.Header.Add("User-Agent", userAgent)
	bOff := &oktaBackoff{
		ctx:             context.TODO(),
		maxRetries:      maxRetries,
		backoffDuration: time.Duration(maxBackoff),
	}
	var tokenResponse *http.Response
	operation := func() error {
		tokenResponse, err = httpClient.Do(tokenRequest)
		bOff.retryCount++
		return err
	}
	err = backoff.Retry(operation, bOff)
	if err != nil {
		return nil, "", nil, err
	}

	respBody, err := io.ReadAll(tokenResponse.Body)
	origResp := io.NopCloser(bytes.NewBuffer(respBody))
	tokenResponse.Body = origResp
	var accessToken *RequestAccessToken

	newClientAssertion, err := createClientAssertion(orgURL, clientID, signer)
	if err != nil {
		return nil, "", nil, err
	}

	if tokenResponse.StatusCode >= 300 {
		if strings.Contains(string(respBody), "invalid_dpop_proof") {
			return getAccessTokenForDpopPrivateKey(tokenRequest, httpClient, orgURL, "", maxRetries, maxBackoff, newClientAssertion, strings.Join(scopes, " "), clientID, signer)
		} else {
			return nil, "", nil, err
		}
	}

	_, err = buildResponse(tokenResponse, nil, &accessToken)
	if err != nil {
		return nil, "", nil, err
	}
	return accessToken, "", nil, nil
}

func getAccessTokenForDpopPrivateKey(tokenRequest *http.Request, httpClient *http.Client, orgURL, nonce string, maxRetries int32, maxBackoff int64, clientAssertion string, scopes string, clientID string, signer jose.Signer) (*RequestAccessToken, string, *rsa.PrivateKey, error) {
	privateKey, err := generatePrivateKey(2048)
	if err != nil {
		return nil, "", nil, err
	}
	dpopJWT, err := generateDpopJWT(privateKey, http.MethodPost, fmt.Sprintf("%v%v", orgURL, "/oauth2/v1/token"), nonce, "")
	if err != nil {
		return nil, "", nil, err
	}
	newClientAssertion, err := createClientAssertion(orgURL, clientID, signer)
	if err != nil {
		return nil, "", nil, err
	}

	query := url.Values{}
	query.Add("grant_type", "client_credentials")
	query.Add("scope", scopes)
	query.Add("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	query.Add("client_assertion", newClientAssertion)
	tokenRequest.Body = io.NopCloser(strings.NewReader(query.Encode()))
	tokenRequest.Header.Set("DPoP", dpopJWT)

	bOff := &oktaBackoff{
		ctx:             context.TODO(),
		maxRetries:      maxRetries,
		backoffDuration: time.Duration(maxBackoff),
	}
	var tokenResponse *http.Response
	operation := func() error {
		tokenResponse, err = httpClient.Do(tokenRequest)
		bOff.retryCount++
		return err
	}
	err = backoff.Retry(operation, bOff)
	if err != nil {
		return nil, "", nil, err
	}
	respBody, err := io.ReadAll(tokenResponse.Body)
	if err != nil {
		return nil, "", nil, err
	}

	if tokenResponse.StatusCode >= 300 {
		if strings.Contains(string(respBody), "use_dpop_nonce") {
			newNonce := tokenResponse.Header.Get("Dpop-Nonce")
			return getAccessTokenForDpopPrivateKey(tokenRequest, httpClient, orgURL, newNonce, maxRetries, maxBackoff, clientAssertion, scopes, clientID, signer)
		} else {
			return nil, "", nil, err
		}
	}
	origResp := io.NopCloser(bytes.NewBuffer(respBody))
	tokenResponse.Body = origResp
	var accessToken *RequestAccessToken
	_, err = buildResponse(tokenResponse, nil, &accessToken)
	return accessToken, nonce, privateKey, nil
}

// NewAPIClient creates a new API client. Requires a userAgent string describing your application.
// optionally a custom http.Client to allow for advanced features such as caching.
func NewAPIClient(cfg *Configuration) *APIClient {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	if cfg.Okta.Client.Proxy.Host != "" {
		var proxyURL url.URL
		proxyURL.Host = fmt.Sprintf("%v:%v", cfg.Okta.Client.Proxy.Host, cfg.Okta.Client.Proxy.Port)
		up := url.UserPassword(cfg.Okta.Client.Proxy.Username, cfg.Okta.Client.Proxy.Password)
		proxyURL.User = up
		transport := http.Transport{Proxy: http.ProxyURL(&proxyURL)}
		cfg.HTTPClient = &http.Client{Transport: &transport}
	}

	var oktaCache Cache
	if !cfg.Okta.Client.Cache.Enabled {
		oktaCache = NewNoOpCache()
	} else {
		if cfg.CacheManager == nil {
			oktaCache = NewGoCache(cfg.Okta.Client.Cache.DefaultTtl,
				cfg.Okta.Client.Cache.DefaultTti)
		} else {
			oktaCache = cfg.CacheManager
		}
	}

	c := &APIClient{}
	c.cfg = cfg
	c.cache = oktaCache
	c.tokenCache = goCache.New(5*time.Minute, 10*time.Minute)
	c.common.client = c

	// API Services
	c.AgentPoolsAPI = (*AgentPoolsAPIService)(&c.common)
	c.ApiServiceIntegrationsAPI = (*ApiServiceIntegrationsAPIService)(&c.common)
	c.ApiTokenAPI = (*ApiTokenAPIService)(&c.common)
	c.ApplicationAPI = (*ApplicationAPIService)(&c.common)
	c.ApplicationConnectionsAPI = (*ApplicationConnectionsAPIService)(&c.common)
	c.ApplicationCredentialsAPI = (*ApplicationCredentialsAPIService)(&c.common)
	c.ApplicationFeaturesAPI = (*ApplicationFeaturesAPIService)(&c.common)
	c.ApplicationGrantsAPI = (*ApplicationGrantsAPIService)(&c.common)
	c.ApplicationGroupsAPI = (*ApplicationGroupsAPIService)(&c.common)
	c.ApplicationLogosAPI = (*ApplicationLogosAPIService)(&c.common)
	c.ApplicationPoliciesAPI = (*ApplicationPoliciesAPIService)(&c.common)
	c.ApplicationSSOAPI = (*ApplicationSSOAPIService)(&c.common)
	c.ApplicationTokensAPI = (*ApplicationTokensAPIService)(&c.common)
	c.ApplicationUsersAPI = (*ApplicationUsersAPIService)(&c.common)
	c.AttackProtectionAPI = (*AttackProtectionAPIService)(&c.common)
	c.AuthenticatorAPI = (*AuthenticatorAPIService)(&c.common)
	c.AuthorizationServerAPI = (*AuthorizationServerAPIService)(&c.common)
	c.AuthorizationServerAssocAPI = (*AuthorizationServerAssocAPIService)(&c.common)
	c.AuthorizationServerClaimsAPI = (*AuthorizationServerClaimsAPIService)(&c.common)
	c.AuthorizationServerClientsAPI = (*AuthorizationServerClientsAPIService)(&c.common)
	c.AuthorizationServerKeysAPI = (*AuthorizationServerKeysAPIService)(&c.common)
	c.AuthorizationServerPoliciesAPI = (*AuthorizationServerPoliciesAPIService)(&c.common)
	c.AuthorizationServerRulesAPI = (*AuthorizationServerRulesAPIService)(&c.common)
	c.AuthorizationServerScopesAPI = (*AuthorizationServerScopesAPIService)(&c.common)
	c.BehaviorAPI = (*BehaviorAPIService)(&c.common)
	c.BrandsAPI = (*BrandsAPIService)(&c.common)
	c.CAPTCHAAPI = (*CAPTCHAAPIService)(&c.common)
	c.CustomDomainAPI = (*CustomDomainAPIService)(&c.common)
	c.CustomPagesAPI = (*CustomPagesAPIService)(&c.common)
	c.CustomTemplatesAPI = (*CustomTemplatesAPIService)(&c.common)
	c.DeviceAPI = (*DeviceAPIService)(&c.common)
	c.DeviceAssuranceAPI = (*DeviceAssuranceAPIService)(&c.common)
	c.DirectoriesIntegrationAPI = (*DirectoriesIntegrationAPIService)(&c.common)
	c.EmailDomainAPI = (*EmailDomainAPIService)(&c.common)
	c.EmailServerAPI = (*EmailServerAPIService)(&c.common)
	c.EventHookAPI = (*EventHookAPIService)(&c.common)
	c.FeatureAPI = (*FeatureAPIService)(&c.common)
	c.GroupAPI = (*GroupAPIService)(&c.common)
	c.GroupOwnerAPI = (*GroupOwnerAPIService)(&c.common)
	c.HookKeyAPI = (*HookKeyAPIService)(&c.common)
	c.IdentityProviderAPI = (*IdentityProviderAPIService)(&c.common)
	c.IdentitySourceAPI = (*IdentitySourceAPIService)(&c.common)
	c.InlineHookAPI = (*InlineHookAPIService)(&c.common)
	c.LinkedObjectAPI = (*LinkedObjectAPIService)(&c.common)
	c.LogStreamAPI = (*LogStreamAPIService)(&c.common)
	c.NetworkZoneAPI = (*NetworkZoneAPIService)(&c.common)
	c.OktaApplicationSettingsAPI = (*OktaApplicationSettingsAPIService)(&c.common)
	c.OrgSettingAPI = (*OrgSettingAPIService)(&c.common)
	c.PolicyAPI = (*PolicyAPIService)(&c.common)
	c.PrincipalRateLimitAPI = (*PrincipalRateLimitAPIService)(&c.common)
	c.ProfileMappingAPI = (*ProfileMappingAPIService)(&c.common)
	c.PushProviderAPI = (*PushProviderAPIService)(&c.common)
	c.RateLimitSettingsAPI = (*RateLimitSettingsAPIService)(&c.common)
	c.RealmAPI = (*RealmAPIService)(&c.common)
	c.RealmAssignmentAPI = (*RealmAssignmentAPIService)(&c.common)
	c.ResourceSetAPI = (*ResourceSetAPIService)(&c.common)
	c.RiskEventAPI = (*RiskEventAPIService)(&c.common)
	c.RiskProviderAPI = (*RiskProviderAPIService)(&c.common)
	c.RoleAPI = (*RoleAPIService)(&c.common)
	c.RoleAssignmentAPI = (*RoleAssignmentAPIService)(&c.common)
	c.RoleTargetAPI = (*RoleTargetAPIService)(&c.common)
	c.SSFReceiverAPI = (*SSFReceiverAPIService)(&c.common)
	c.SSFSecurityEventTokenAPI = (*SSFSecurityEventTokenAPIService)(&c.common)
	c.SSFTransmitterAPI = (*SSFTransmitterAPIService)(&c.common)
	c.SchemaAPI = (*SchemaAPIService)(&c.common)
	c.SessionAPI = (*SessionAPIService)(&c.common)
	c.SubscriptionAPI = (*SubscriptionAPIService)(&c.common)
	c.SystemLogAPI = (*SystemLogAPIService)(&c.common)
	c.TemplateAPI = (*TemplateAPIService)(&c.common)
	c.ThemesAPI = (*ThemesAPIService)(&c.common)
	c.ThreatInsightAPI = (*ThreatInsightAPIService)(&c.common)
	c.TrustedOriginAPI = (*TrustedOriginAPIService)(&c.common)
	c.UISchemaAPI = (*UISchemaAPIService)(&c.common)
	c.UserAPI = (*UserAPIService)(&c.common)
	c.UserFactorAPI = (*UserFactorAPIService)(&c.common)
	c.UserTypeAPI = (*UserTypeAPIService)(&c.common)

	return c
}

func atoi(in string) (int, error) {
	return strconv.Atoi(in)
}

// selectHeaderContentType select a content type from the available list.
func selectHeaderContentType(contentTypes []string) string {
	if len(contentTypes) == 0 {
		return ""
	}
	if contains(contentTypes, "application/json") {
		return "application/json"
	}
	return contentTypes[0] // use the first content type specified in 'consumes'
}

// selectHeaderAccept join all accept types and return
func selectHeaderAccept(accepts []string) string {
	if len(accepts) == 0 {
		return ""
	}

	//if contains(accepts, "application/json") {
	//	return "application/json"
	//}

	return strings.Join(accepts, ",")
}

// contains is a case insensitive match, finding needle in a haystack
func contains(haystack []string, needle string) bool {
	for _, a := range haystack {
		if strings.ToLower(a) == strings.ToLower(needle) {
			return true
		}
	}
	return false
}

// Verify optional parameters are of the correct type.
func typeCheckParameter(obj interface{}, expected string, name string) error {
	// Make sure there is an object.
	if obj == nil {
		return nil
	}

	// Check the type is as expected.
	if reflect.TypeOf(obj).String() != expected {
		return fmt.Errorf("Expected %s to be of type %s but received %s.", name, expected, reflect.TypeOf(obj).String())
	}
	return nil
}

// parameterToString convert interface{} parameters to string, using a delimiter if format is provided.
func parameterToString(obj interface{}, collectionFormat string) string {
	var delimiter string

	switch collectionFormat {
	case "pipes":
		delimiter = "|"
	case "ssv":
		delimiter = " "
	case "tsv":
		delimiter = "\t"
	case "csv":
		delimiter = ","
	}

	if reflect.TypeOf(obj).Kind() == reflect.Slice {
		return strings.Trim(strings.Replace(fmt.Sprint(obj), " ", delimiter, -1), "[]")
	} else if t, ok := obj.(time.Time); ok {
		return t.Format(time.RFC3339)
	}

	return fmt.Sprintf("%v", obj)
}

// helper for converting interface{} parameters to json strings
func parameterToJson(obj interface{}) (string, error) {
	jsonBuf, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(jsonBuf), err
}

// callAPI do the request.
func (c *APIClient) callAPI(request *http.Request) (*http.Response, error) {
	if c.cfg.Debug {
		dump, err := httputil.DumpRequestOut(request, true)
		if err != nil {
			return nil, err
		}
		log.Printf("\n%s\n", string(dump))
	}

	resp, err := c.cfg.HTTPClient.Do(request)
	if err != nil {
		return resp, err
	}

	if c.cfg.Debug {
		dump, err := httputil.DumpResponse(resp, true)
		if err != nil {
			return resp, err
		}
		log.Printf("\n%s\n", string(dump))
	}
	return resp, err
}

// Allow modification of underlying config for alternate implementations and testing
// Caution: modifying the configuration while live can cause data races and potentially unwanted behavior
func (c *APIClient) GetConfig() *Configuration {
	return c.cfg
}

type formFile struct {
	fileBytes    []byte
	fileName     string
	formFileName string
}

// prepareRequest build the request
func (c *APIClient) prepareRequest(
	ctx context.Context,
	path string, method string,
	postBody interface{},
	headerParams map[string]string,
	queryParams url.Values,
	formParams url.Values,
	formFiles []formFile) (localVarRequest *http.Request, err error) {

	var body *bytes.Buffer

	// Detect postBody type and post.
	if postBody != nil {
		contentType := headerParams["Content-Type"]
		if contentType == "" {
			contentType = detectContentType(postBody)
			headerParams["Content-Type"] = contentType
		}

		body, err = setBody(postBody, contentType)
		if err != nil {
			return nil, err
		}
	}

	// add form parameters and file if available.
	if strings.HasPrefix(headerParams["Content-Type"], "multipart/form-data") && len(formParams) > 0 || (len(formFiles) > 0) {
		if body != nil {
			return nil, errors.New("Cannot specify postBody and multipart form at the same time.")
		}
		body = &bytes.Buffer{}
		w := multipart.NewWriter(body)

		for k, v := range formParams {
			for _, iv := range v {
				if strings.HasPrefix(k, "@") { // file
					err = addFile(w, k[1:], iv)
					if err != nil {
						return nil, err
					}
				} else { // form value
					w.WriteField(k, iv)
				}
			}
		}
		for _, formFile := range formFiles {
			if len(formFile.fileBytes) > 0 && formFile.fileName != "" {
				w.Boundary()
				part, err := w.CreateFormFile(formFile.formFileName, filepath.Base(formFile.fileName))
				if err != nil {
					return nil, err
				}
				_, err = part.Write(formFile.fileBytes)
				if err != nil {
					return nil, err
				}
			}
		}

		// Set the Boundary in the Content-Type
		headerParams["Content-Type"] = w.FormDataContentType()

		// Set Content-Length
		headerParams["Content-Length"] = fmt.Sprintf("%d", body.Len())
		w.Close()
	}

	if strings.HasPrefix(headerParams["Content-Type"], "application/x-www-form-urlencoded") && len(formParams) > 0 {
		if body != nil {
			return nil, errors.New("Cannot specify postBody and x-www-form-urlencoded form at the same time.")
		}
		body = &bytes.Buffer{}
		body.WriteString(formParams.Encode())
		// Set Content-Length
		headerParams["Content-Length"] = fmt.Sprintf("%d", body.Len())
	}

	// Setup path and query parameters
	URL, err := url.Parse(path)
	if err != nil {
		return nil, err
	}

	// Override request host, if applicable
	if c.cfg.Host != "" {
		URL.Host = c.cfg.Host
	}

	// Override request scheme, if applicable
	if c.cfg.Scheme != "" {
		URL.Scheme = c.cfg.Scheme
	}

	urlWithoutQuery := *URL

	// Adding Query Param
	query := URL.Query()
	for k, v := range queryParams {
		for _, iv := range v {
			query.Add(k, iv)
		}
	}

	// Encode the parameters.
	URL.RawQuery = query.Encode()

	// Generate a new request
	if body != nil {
		localVarRequest, err = http.NewRequest(method, URL.String(), body)
	} else {
		localVarRequest, err = http.NewRequest(method, URL.String(), nil)
	}
	if err != nil {
		return nil, err
	}

	// add header parameters, if any
	if len(headerParams) > 0 {
		headers := http.Header{}
		for h, v := range headerParams {
			headers[h] = []string{v}
		}
		localVarRequest.Header = headers
	}

	// Add the user agent to the request.
	localVarRequest.Header.Add("User-Agent", NewUserAgent(c.cfg).String())

	if ctx != nil {
		// add context to the request
		localVarRequest = localVarRequest.WithContext(ctx)

		// Walk through any authentication.

		// OAuth2 authentication
		if tok, ok := ctx.Value(ContextOAuth2).(oauth2.TokenSource); ok {
			// We were able to grab an oauth2 token from the context
			var latestToken *oauth2.Token
			if latestToken, err = tok.Token(); err != nil {
				return nil, err
			}

			latestToken.SetAuthHeader(localVarRequest)
		}

		// Basic HTTP Authentication
		if auth, ok := ctx.Value(ContextBasicAuth).(BasicAuth); ok {
			localVarRequest.SetBasicAuth(auth.UserName, auth.Password)
		}

		// AccessToken Authentication
		if auth, ok := ctx.Value(ContextAccessToken).(string); ok {
			localVarRequest.Header.Add("Authorization", "Bearer "+auth)
		}

	}

	// This will override the auth in context
	var auth Authorization
	switch c.cfg.Okta.Client.AuthorizationMode {
	case "SSWS":
		auth = NewSSWSAuth(c.cfg.Okta.Client.Token, localVarRequest)
	case "Bearer":
		auth = NewBearerAuth(c.cfg.Okta.Client.Token, localVarRequest)
	case "PrivateKey":
		auth = NewPrivateKeyAuth(PrivateKeyAuthConfig{
			TokenCache:       c.tokenCache,
			HttpClient:       c.cfg.HTTPClient,
			PrivateKeySigner: c.cfg.PrivateKeySigner,
			PrivateKey:       c.cfg.Okta.Client.PrivateKey,
			PrivateKeyId:     c.cfg.Okta.Client.PrivateKeyId,
			ClientId:         c.cfg.Okta.Client.ClientId,
			OrgURL:           c.cfg.Okta.Client.OrgUrl,
			UserAgent:        NewUserAgent(c.cfg).String(),
			Scopes:           c.cfg.Okta.Client.Scopes,
			MaxRetries:       c.cfg.Okta.Client.RateLimit.MaxRetries,
			MaxBackoff:       c.cfg.Okta.Client.RateLimit.MaxBackoff,
			Req:              localVarRequest,
		})
	case "JWT":
		auth = NewJWTAuth(JWTAuthConfig{
			TokenCache:      c.tokenCache,
			HttpClient:      c.cfg.HTTPClient,
			OrgURL:          c.cfg.Okta.Client.OrgUrl,
			UserAgent:       NewUserAgent(c.cfg).String(),
			Scopes:          c.cfg.Okta.Client.Scopes,
			ClientAssertion: c.cfg.Okta.Client.ClientAssertion,
			MaxRetries:      c.cfg.Okta.Client.RateLimit.MaxRetries,
			MaxBackoff:      c.cfg.Okta.Client.RateLimit.MaxBackoff,
			Req:             localVarRequest,
		})
	case "JWK":
		auth = NewJWKAuth(JWKAuthConfig{
			TokenCache:       c.tokenCache,
			HttpClient:       c.cfg.HTTPClient,
			JWK:              c.cfg.Okta.Client.JWK,
			EncryptionType:   c.cfg.Okta.Client.EncryptionType,
			PrivateKeySigner: c.cfg.PrivateKeySigner,
			PrivateKeyId:     c.cfg.Okta.Client.PrivateKeyId,
			ClientId:         c.cfg.Okta.Client.ClientId,
			OrgURL:           c.cfg.Okta.Client.OrgUrl,
			UserAgent:        NewUserAgent(c.cfg).String(),
			Scopes:           c.cfg.Okta.Client.Scopes,
			MaxRetries:       c.cfg.Okta.Client.RateLimit.MaxRetries,
			MaxBackoff:       c.cfg.Okta.Client.RateLimit.MaxBackoff,
			Req:              localVarRequest,
		})
	default:
		return nil, fmt.Errorf("unknown authorization mode %v", c.cfg.Okta.Client.AuthorizationMode)
	}
	err = auth.Authorize(method, urlWithoutQuery.String())
	if err != nil {
		return nil, err
	}

	for header, value := range c.cfg.DefaultHeader {
		localVarRequest.Header.Add(header, value)
	}
	return localVarRequest, nil
}

func (c *APIClient) decode(v interface{}, b []byte, contentType string) (err error) {
	if len(b) == 0 {
		return nil
	}
	if s, ok := v.(*string); ok {
		*s = string(b)
		return nil
	}
	if f, ok := v.(**os.File); ok {
		*f, err = ioutil.TempFile("", "HttpClientFile")
		if err != nil {
			return
		}
		_, err = (*f).Write(b)
		if err != nil {
			return
		}
		_, err = (*f).Seek(0, io.SeekStart)
		return
	}
	if xmlCheck.MatchString(contentType) {
		if err = xml.Unmarshal(b, v); err != nil {
			return err
		}
		return nil
	}
	if jsonCheck.MatchString(contentType) {
		if actualObj, ok := v.(interface{ GetActualInstance() interface{} }); ok { // oneOf, anyOf schemas
			if unmarshalObj, ok := actualObj.(interface{ UnmarshalJSON([]byte) error }); ok { // make sure it has UnmarshalJSON defined
				if err = unmarshalObj.UnmarshalJSON(b); err != nil {
					return err
				}
			} else {
				return errors.New("Unknown type with GetActualInstance but no unmarshalObj.UnmarshalJSON defined")
			}
		} else if err = json.Unmarshal(b, v); err != nil { // simple model
			return err
		}
		return nil
	}
	return errors.New("undefined response type")
}

func (c *APIClient) RefreshNext() *APIClient {
	c.freshcache = true
	return c
}

func (c *APIClient) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	cacheKey := CreateCacheKey(req)
	if req.Method != http.MethodGet {
		c.cache.Delete(cacheKey)
	}
	inCache := c.cache.Has(cacheKey)
	if c.freshcache {
		c.cache.Delete(cacheKey)
		inCache = false
		c.freshcache = false
	}
	if !inCache {
		if c.cfg.Okta.Client.RateLimit.Enable {
			c.rateLimitLock.Lock()
			limit := c.rateLimit
			c.rateLimitLock.Unlock()
			if limit != nil && limit.Remaining <= 0 {
				timer := time.NewTimer(time.Second * time.Duration(limit.Reset))
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return nil, ctx.Err()
				case <-timer.C:
				}
			}
		}
		resp, err := c.doWithRetries(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 && req.Method == http.MethodGet {
			if c.cfg.Okta.Client.RateLimit.Enable {
				c.rateLimitLock.Lock()
				newLimit, err := c.parseLimitHeaders(resp)
				if err == nil {
					c.rateLimit = newLimit
				}
				c.rateLimitLock.Unlock()
			}
			c.cache.Set(cacheKey, resp)
		}
		return resp, err
	}
	return c.cache.Get(cacheKey), nil
}

func (c *APIClient) doWithRetries(ctx context.Context, req *http.Request) (*http.Response, error) {
	var bodyReader func() io.ReadCloser
	if req.Body != nil {
		buf, err := ioutil.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodyReader = func() io.ReadCloser {
			return ioutil.NopCloser(bytes.NewReader(buf))
		}
	}
	var (
		resp *http.Response
		err  error
	)
	bOff := &oktaBackoff{
		ctx:        ctx,
		maxRetries: c.cfg.Okta.Client.RateLimit.MaxRetries,
	}
	operation := func() error {
		// Always rewind the request body when non-nil.
		if bodyReader != nil {
			req.Body = bodyReader()
		}
		resp, err = c.callAPI(req)
		if errors.Is(err, io.EOF) {
			// retry on EOF errors, which might be caused by network connectivity issues
			return fmt.Errorf("network error: %w", err)
		} else if err != nil {
			// this is error is considered to be permanent and should not be retried
			return backoff.Permanent(err)
		}
		if !tooManyRequests(resp) {
			return nil
		}
		if err = tryDrainBody(resp.Body); err != nil {
			return err
		}
		backoffDuration, err := Get429BackoffTime(resp)
		if err != nil {
			return err
		}
		if c.cfg.Okta.Client.RateLimit.MaxBackoff < backoffDuration {
			backoffDuration = c.cfg.Okta.Client.RateLimit.MaxBackoff
		}
		bOff.backoffDuration = time.Second * time.Duration(backoffDuration)
		bOff.retryCount++
		req.Header.Add("X-Okta-Retry-For", resp.Header.Get("X-Okta-Request-Id"))
		req.Header.Add("X-Okta-Retry-Count", fmt.Sprint(bOff.retryCount))
		return errors.New("too many requests")
	}
	err = backoff.Retry(operation, bOff)
	return resp, err
}

// Add a file to the multipart request
func addFile(w *multipart.Writer, fieldName, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	part, err := w.CreateFormFile(fieldName, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)

	return err
}

// Prevent trying to import "fmt"
func reportError(format string, a ...interface{}) error {
	return fmt.Errorf(format, a...)
}

// A wrapper for strict JSON decoding
func newStrictDecoder(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewBuffer(data))
	dec.DisallowUnknownFields()
	return dec
}

// Set request body from an interface{}
func setBody(body interface{}, contentType string) (bodyBuf *bytes.Buffer, err error) {
	if bodyBuf == nil {
		bodyBuf = &bytes.Buffer{}
	}

	if reader, ok := body.(io.Reader); ok {
		_, err = bodyBuf.ReadFrom(reader)
	} else if fp, ok := body.(**os.File); ok {
		_, err = bodyBuf.ReadFrom(*fp)
	} else if b, ok := body.([]byte); ok {
		_, err = bodyBuf.Write(b)
	} else if s, ok := body.(string); ok {
		_, err = bodyBuf.WriteString(s)
	} else if s, ok := body.(*string); ok {
		_, err = bodyBuf.WriteString(*s)
	} else if jsonCheck.MatchString(contentType) {
		err = json.NewEncoder(bodyBuf).Encode(body)
	} else if xmlCheck.MatchString(contentType) {
		err = xml.NewEncoder(bodyBuf).Encode(body)
	}

	if err != nil {
		return nil, err
	}

	if bodyBuf.Len() == 0 {
		err = fmt.Errorf("Invalid body type %s\n", contentType)
		return nil, err
	}
	return bodyBuf, nil
}

// detectContentType method is used to figure out `Request.Body` content type for request header
func detectContentType(body interface{}) string {
	contentType := "text/plain; charset=utf-8"
	kind := reflect.TypeOf(body).Kind()

	switch kind {
	case reflect.Struct, reflect.Map, reflect.Ptr:
		contentType = "application/json; charset=utf-8"
	case reflect.String:
		contentType = "text/plain; charset=utf-8"
	default:
		if b, ok := body.([]byte); ok {
			contentType = http.DetectContentType(b)
		} else if kind == reflect.Slice {
			contentType = "application/json; charset=utf-8"
		}
	}

	return contentType
}

// Ripped from https://github.com/gregjones/httpcache/blob/master/httpcache.go
type cacheControl map[string]string

func parseCacheControl(headers http.Header) cacheControl {
	cc := cacheControl{}
	ccHeader := headers.Get("Cache-Control")
	for _, part := range strings.Split(ccHeader, ",") {
		part = strings.Trim(part, " ")
		if part == "" {
			continue
		}
		if strings.ContainsRune(part, '=') {
			keyval := strings.Split(part, "=")
			cc[strings.Trim(keyval[0], " ")] = strings.Trim(keyval[1], ",")
		} else {
			cc[part] = ""
		}
	}
	return cc
}

// CacheExpires helper function to determine remaining time before repeating a request.
func CacheExpires(r *http.Response) time.Time {
	// Figure out when the cache expires.
	var expires time.Time
	now, err := time.Parse(time.RFC1123, r.Header.Get("date"))
	if err != nil {
		return time.Now()
	}
	respCacheControl := parseCacheControl(r.Header)

	if maxAge, ok := respCacheControl["max-age"]; ok {
		lifetime, err := time.ParseDuration(maxAge + "s")
		if err != nil {
			expires = now
		} else {
			expires = now.Add(lifetime)
		}
	} else {
		expiresHeader := r.Header.Get("Expires")
		if expiresHeader != "" {
			expires, err = time.Parse(time.RFC1123, expiresHeader)
			if err != nil {
				expires = now
			}
		}
	}
	return expires
}

func strlen(s string) int {
	return utf8.RuneCountInString(s)
}

// GenericOpenAPIError Provides access to the body, error and model on returned errors.
type GenericOpenAPIError struct {
	body  []byte
	error string
	model interface{}
}

// Error returns non-empty string if there was an error.
func (e GenericOpenAPIError) Error() string {
	return e.error
}

// Body returns the raw bytes of the response
func (e GenericOpenAPIError) Body() []byte {
	return e.body
}

// Model returns the unpacked model of the error
func (e GenericOpenAPIError) Model() interface{} {
	return e.model
}

// Okta Backoff
type oktaBackoff struct {
	retryCount, maxRetries int32
	backoffDuration        time.Duration
	ctx                    context.Context
}

// NextBackOff returns the duration to wait before retrying the operation,
// or backoff. Stop to indicate that no more retries should be made.
func (o *oktaBackoff) NextBackOff() time.Duration {
	// stop retrying if operation reached retry limit
	if o.retryCount > o.maxRetries {
		return backoff.Stop
	}
	return o.backoffDuration
}

// Reset to initial state.
func (o *oktaBackoff) Reset() {}

func (o *oktaBackoff) Context() context.Context {
	return o.ctx
}

func tooManyRequests(resp *http.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusTooManyRequests
}

func tryDrainBody(body io.ReadCloser) error {
	defer body.Close()
	_, err := io.Copy(ioutil.Discard, io.LimitReader(body, 4096))
	return err
}

func Get429BackoffTime(resp *http.Response) (int64, error) {
	requestDate, err := time.Parse("Mon, 02 Jan 2006 15:04:05 GMT", resp.Header.Get("Date"))
	if err != nil {
		// this is error is considered to be permanent and should not be retried
		return 0, backoff.Permanent(fmt.Errorf("date header is missing or invalid: %w", err))
	}
	rateLimitReset, err := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Reset"))
	if err != nil {
		// this is error is considered to be permanent and should not be retried
		return 0, backoff.Permanent(fmt.Errorf("X-Rate-Limit-Reset header is missing or invalid: %w", err))
	}
	return int64(rateLimitReset) - requestDate.Unix() + 1, nil
}

type ClientAssertionClaims struct {
	Issuer   string           `json:"iss,omitempty"`
	Subject  string           `json:"sub,omitempty"`
	Audience string           `json:"aud,omitempty"`
	Expiry   *jwt.NumericDate `json:"exp,omitempty"`
	IssuedAt *jwt.NumericDate `json:"iat,omitempty"`
	ID       string           `json:"jti,omitempty"`
}

type RequestAccessToken struct {
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

func generatePrivateKey(bitSize int) (*rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, bitSize)
	if err != nil {
		return nil, err
	}
	err = privateKey.Validate()
	if err != nil {
		return nil, err
	}
	return privateKey, nil
}

func privateKeyToBytes(priv *rsa.PrivateKey) []byte {
	privBytes := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(priv),
		},
	)
	return privBytes
}

func publicKeyToBytes(priv *rsa.PrivateKey) []byte {
	privBytes := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PUBLIC KEY",
			Bytes: x509.MarshalPKCS1PublicKey(&priv.PublicKey),
		},
	)
	return privBytes
}

type DpopClaims struct {
	HTTPMethod  string           `json:"htm,omitempty"`
	HTTPURI     string           `json:"htu,omitempty"`
	IssuedAt    *jwt.NumericDate `json:"iat,omitempty"`
	Nonce       string           `json:"nonce,omitempty"`
	ID          string           `json:"jti,omitempty"`
	AccessToken string           `json:"ath,omitempty"`
}

func generateDpopJWT(privateKey *rsa.PrivateKey, httpMethod, URL, nonce, accessToken string) (string, error) {
	set, err := jwk.Import(privateKey.PublicKey)
	if err != nil {
		return "", err
	}
	err = jwk.AssignKeyID(set)
	if err != nil {
		return "", err
	}
	key := jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}
	signerOpts := jose.SignerOptions{}
	signerOpts.WithType("dpop+jwt")
	signerOpts.WithHeader("jwk", set)
	rsaSigner, err := jose.NewSigner(key, &signerOpts)
	if err != nil {
		return "", err
	}
	dpopClaims := DpopClaims{
		ID:         uuid.New().String(),
		HTTPMethod: httpMethod,
		HTTPURI:    URL,
		IssuedAt:   jwt.NewNumericDate(time.Now()),
		Nonce:      nonce,
	}
	if accessToken != "" {
		h := sha256.New()
		h.Write(StringToAsciiBytes(accessToken))
		dpopClaims.AccessToken = base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	}
	jwtBuilder := jwt.Signed(rsaSigner).Claims(dpopClaims)
	return jwtBuilder.CompactSerialize()
}

func StringToAsciiBytes(s string) []byte {
	t := make([]byte, utf8.RuneCountInString(s))
	i := 0
	for _, r := range s {
		t[i] = byte(r)
		i++
	}
	return t
}

func (c *APIClient) parseLimitHeaders(resp *http.Response) (*RateLimit, error) {
	limit, err := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Limit"))
	if err != nil {
		return nil, err
	}
	remaining, err := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Remaining"))
	if err != nil {
		return nil, err
	}
	reset, err := Get429BackoffTime(resp)
	if err != nil {
		return nil, err
	}
	return &RateLimit{
		Limit:     limit,
		Remaining: remaining,
		Reset:     reset,
	}, nil
}
