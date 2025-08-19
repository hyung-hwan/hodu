package main

import "crypto/rsa"
import "crypto/tls"
import "crypto/x509"
import "encoding/base64"
import "encoding/pem"
import "fmt"
import "hodu"
import "net/netip"
import "os"
import "strings"
import "time"

//import "gopkg.in/yaml.v3"
import yaml "github.com/goccy/go-yaml"


type ServerTLSConfig struct {
	Enabled                  bool               `yaml:"enabled"`
	CertFile                 string             `yaml:"cert-file"`
	KeyFile                  string             `yaml:"key-file"`
	CertText                 string             `yaml:"cert-text"`
	KeyText                  string             `yaml:"key-text"`

	ClientAuthType           string             `yaml:"client-auth-type"`
	ClientCACertFile         string             `yaml:"client-ca-cert-file"`
	ClientCACertText         string             `yaml:"client-ca-cert-text"`
	//CipherSuites             []Cipher           `yaml:"cipher_suites"`
	//CurvePreferences         []Curve            `yaml:"curve_preferences"`
	//MinVersion               TLSVersion         `yaml:"min-version"`
	//MaxVersion               TLSVersion         `yaml:"max-version"`
	//PreferServerCipherSuites bool               `yaml:"prefer-server-cipher-suites"`
	//ClientAllowedSans        []string           `yaml:"client-allowed-sans"`
}

type ClientTLSConfig struct {
	Enabled            bool    `yaml:"enabled"`
	CertFile           string  `yaml:"cert-file"`
	KeyFile            string  `yaml:"key-file"`
	CertText           string  `yaml:"cert-text"`
	KeyText            string  `yaml:"key-text"`
	ServerCACertFile   string  `yaml:"server-ca-cert-file"`
	ServerCACertText   string  `yaml:"server-ca-cert-text"`
	InsecureSkipVerify bool    `yaml:"skip-verify"`
	ServerName         string  `yaml:"server-name"`
}

type HttpAccessRule struct {
	Prefix string `yaml:"prefix"`
	OrgNets []string `yaml:"origin-networks"`
	Action string `yaml:"action"`
}

type HttpAuthConfig struct {
	Enabled bool `yaml:"enabled"`
	Realm string `yaml:"realm"`
	Creds []string `yaml:"credentials"`
	TokenTtl string `yaml:"token-ttl"`
	TokenRsaKeyText string `yaml:"token-rsa-key-text"`
	TokenRsaKeyFile string `yaml:"token-rsa-key-file"`
	AccessRules []HttpAccessRule `yaml:"access-rules"`
}

type CTLServiceConfig struct {
	Prefix string   `yaml:"prefix"`  // url prefix for control channel endpoints
	Addrs  []string `yaml:"addresses"`
	Cors   bool     `yaml:"cors"`
	Auth   HttpAuthConfig `yaml:"auth"`
}

type RPXServiceConfig struct {
	Addrs  []string `yaml:"addresses"`
}

type RPXClientTokenConfig struct {
	AttrName string `yaml:"attr-name"`
	Regex string `yaml:"regex"`	
	SubmatchIndex int `yaml:"submatch-index"`
}

type PXYServiceConfig struct {
	Addrs  []string `yaml:"addresses"`
}

type WPXServiceConfig struct {
	Addrs  []string `yaml:"addresses"`
}

type RPCServiceConfig struct { // rpc server-side configuration
	Addrs     []string  `yaml:"addresses"`
}

type RPCEndpointConfig struct { // rpc client-side configuration
	Authority   string        `yaml:"authority"`
	Addrs       []string      `yaml:"addresses"`
	SeedTmout   time.Duration `yaml:"seed-timeout"`
}

type ServerAppConfig  struct {
	LogMask       []string  `yaml:"log-mask"`
	LogFile       string    `yaml:"log-file"`
	LogMaxSize    int64     `yaml:"log-max-size"`
	LogRotate     int       `yaml:"log-rotate"`
	MaxPeers      int       `yaml:"max-peer-conns"` // maximum number of connections from peers
	MaxRpcConns   int       `yaml:"max-rpc-conns"` // maximum number of rpc connections
	PtyUser       string    `yaml:"pty-user"`
	PtyShell      string    `yaml:"pty-shell"`
	XtermHtmlFile string    `yaml:"xterm-html-file"`
}

type ClientAppConfig  struct {
	LogMask       []string      `yaml:"log-mask"`
	LogFile       string        `yaml:"log-file"`
	LogMaxSize    int64         `yaml:"log-max-size"`
	LogRotate     int           `yaml:"log-rotate"`
	MaxPeers      int           `yaml:"max-peer-conns"` // maximum number of connections from peers
	MaxRpcConns   int           `yaml:"max-rpc-conns"` // maximum number of rpc connections
	PeerConnTmout time.Duration `yaml:"peer-conn-timeout"`
	TokenText     string        `yaml:"token-text"`
	TokenFile     string        `yaml:"token-file"`
	PtyUser       string        `yaml:"pty-user"`
	PtyShell      string        `yaml:"pty-shell"`
	XtermHtmlFile string        `yaml:"xterm-html-file"`
}

type ServerConfig struct {
	APP ServerAppConfig             `yaml:"app"`

	CTL struct {
		Service CTLServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"ctl"`

	RPX struct {
		Service RPXServiceConfig         `yaml:"service"`
		TLS ServerTLSConfig              `yaml:"tls"`
		ClientToken RPXClientTokenConfig `yaml:"client-token"`
	} `yaml:"rpx"`

	PXY struct {
		Service PXYServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"pxy"`

	WPX struct {
		Service WPXServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"wpx"`

	RPC struct {
		Service RPCServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"rpc"`
}

type ClientConfig struct {
	APP ClientAppConfig             `yaml:"app"`

	CTL struct {
		Service CTLServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"ctl"`
	RPC struct {
		Endpoint RPCEndpointConfig  `yaml:"endpoint"`
		TLS ClientTLSConfig         `yaml:"tls"`
	} `yaml:"rpc"`
	RPX struct {
          Target struct {
			Addr string 	        `yaml:"address"`
			TLS ClientTLSConfig    `yaml:"tls"`
		} `yaml:"target"`
	}
}

func load_server_config_to(cfgfile string, cfg *ServerConfig) error {
	var f *os.File
	var yd *yaml.Decoder
	var err error

	f, err = os.Open(cfgfile)
	if err != nil { return err }

	yd = yaml.NewDecoder(f, yaml.AllowDuplicateMapKey(), yaml.DisallowUnknownField())
	err = yd.Decode(cfg)
	f.Close()
	return err
}

func load_client_config_to(cfgfile string, cfg *ClientConfig) error {
	var f *os.File
	var yd *yaml.Decoder
	var err error

	f, err = os.Open(cfgfile)
	if err != nil { return err }

	yd = yaml.NewDecoder(f, yaml.AllowDuplicateMapKey(), yaml.DisallowUnknownField())
	err = yd.Decode(cfg)
	f.Close()
	return err
}


func tls_string_to_client_auth_type(str string) tls.ClientAuthType {
	switch str {
		case tls.NoClientCert.String():
			return tls.NoClientCert
		case tls.RequestClientCert.String():
			return tls.RequestClientCert
		case tls.RequireAnyClientCert.String():
			return tls.RequireAnyClientCert
		case tls.VerifyClientCertIfGiven.String():
			return tls.VerifyClientCertIfGiven
		case tls.RequireAndVerifyClientCert.String():
			return tls.RequireAndVerifyClientCert
		default:
			return tls.NoClientCert
	}
}

func log_strings_to_mask(str []string) hodu.LogMask {

	var mask hodu.LogMask

	if len(str) > 0 {
		var name string

		mask = hodu.LogMask(0)
		for _, name = range str {

			switch name {
				case "all":
					mask = hodu.LOG_ALL

				case "none":
					mask = hodu.LOG_NONE

				case "debug":
					mask |= hodu.LogMask(hodu.LOG_DEBUG)
				case "info":
					mask |= hodu.LogMask(hodu.LOG_INFO)
				case "warn":
					mask |= hodu.LogMask(hodu.LOG_WARN)
				case "error":
					mask |= hodu.LogMask(hodu.LOG_ERROR)
			}
		}
	} else {
		// if not specified, log messages of all levels
		mask = hodu.LOG_ALL
	}

	return mask
}

// --------------------------------------------------------------------

func make_tls_server_config(cfg *ServerTLSConfig) (*tls.Config, error) {
	var tlscfg *tls.Config

	if cfg.Enabled {
		var cert tls.Certificate
		var cert_pool *x509.CertPool
		var ok bool
		var err error

		if cfg.CertText != "" && cfg.KeyText != "" {
			cert, err = tls.X509KeyPair([]byte(cfg.CertText), []byte(cfg.KeyText))
		} else if cfg.CertFile != "" && cfg.KeyFile != "" {
			cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		} else {
			// use the embedded certificate
			cert, err = tls.X509KeyPair(hodu_tls_cert_text, hodu_tls_key_text)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load key pair - %s", err.Error())
		}

		cert_pool = x509.NewCertPool()
		if cfg.ClientCACertText != "" {
			ok = cert_pool.AppendCertsFromPEM([]byte(cfg.ClientCACertText))
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else if cfg.ClientCACertFile != "" {
			var text []byte
			text, err = os.ReadFile(cfg.ClientCACertFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load ca certficate file %s - %s", cfg.ClientCACertFile, err.Error())
			}
			ok = cert_pool.AppendCertsFromPEM(text)
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else {
			ok = cert_pool.AppendCertsFromPEM(hodu_tls_cert_text)
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		}

		tlscfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			// If multiple certificates are configured, we may have to implement GetCertificate
			// GetCertificate: func (chi *tls.ClientHelloInfo) (*Certificate, error) { return cert, nil }
			ClientAuth: tls_string_to_client_auth_type(cfg.ClientAuthType),
			ClientCAs: cert_pool, // trusted CA certs for client certificate verification
		}
	}

	return tlscfg, nil
}

// --------------------------------------------------------------------

func make_tls_client_config(cfg *ClientTLSConfig) (*tls.Config, error) {
	var tlscfg *tls.Config

	if cfg.Enabled {
		var cert tls.Certificate
		var cert_pool *x509.CertPool
		var ok bool
		var err error

		if cfg.CertText != "" && cfg.KeyText != "" {
			cert, err = tls.X509KeyPair([]byte(cfg.CertText), []byte(cfg.KeyText))
		} else if cfg.CertFile != "" && cfg.KeyFile != "" {
			cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		} else {
			// use the embedded certificate
			cert, err = tls.X509KeyPair(hodu_tls_cert_text, hodu_tls_key_text)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load key pair - %s", err.Error())
		}

		cert_pool = x509.NewCertPool()
		if cfg.ServerCACertText != "" {
			ok = cert_pool.AppendCertsFromPEM([]byte(cfg.ServerCACertText))
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else if cfg.ServerCACertFile != "" {
			var text []byte
			text, err = os.ReadFile(cfg.ServerCACertFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load ca certficate file %s - %s", cfg.ServerCACertFile, err.Error())
			}
			ok = cert_pool.AppendCertsFromPEM(text)
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else {
			// trust the embedded certificate if not explicitly specified
			ok = cert_pool.AppendCertsFromPEM(hodu_tls_cert_text)
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		}

		if cfg.ServerName == "" { cfg.ServerName = HODU_NAME }
		tlscfg = &tls.Config{
			//Certificates: []tls.Certificate{cert},
			GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) { return &cert, nil },
			RootCAs: cert_pool,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			ServerName: cfg.ServerName,
		}
	}

	return tlscfg, nil
}

// --------------------------------------------------------------------
func make_http_auth_config(cfg *HttpAuthConfig) (*hodu.HttpAuthConfig, error) {
	var config hodu.HttpAuthConfig
	var cred string
	var b []byte
	var x []string
	var rsa_key_text []byte
	var rk *rsa.PrivateKey
	var pb *pem.Block
	var rule HttpAccessRule
	var idx int
	var err error

	config.Enabled = cfg.Enabled
	config.Realm = cfg.Realm
	config.Creds = make(hodu.HttpAuthCredMap)
	config.TokenTtl, err = hodu.ParseDurationString(cfg.TokenTtl)
	if err != nil {
		return nil, fmt.Errorf("invalid token ttl %s - %s", cred, err.Error())
	}

	// convert user credentials
	for _, cred = range cfg.Creds {
		b, err = base64.StdEncoding.DecodeString(cred)
		if err == nil { cred = string(b) }

		// each entry must be of the form username:password
		x = strings.Split(cred, ":")
		if len(x) != 2 {
			return nil, fmt.Errorf("invalid auth credential - %s", cred)
		}

		config.Creds[x[0]] = x[1]
	}

	// load rsa key
	if cfg.TokenRsaKeyText == "" && cfg.TokenRsaKeyFile != "" {
		rsa_key_text, err = os.ReadFile(cfg.TokenRsaKeyFile)
		if err != nil {
			return nil, fmt.Errorf("unable to read %s - %s", cfg.TokenRsaKeyFile, err.Error())
		}
	}
	if len(rsa_key_text) == 0 { rsa_key_text = []byte(cfg.TokenRsaKeyText) }
	if len(rsa_key_text) == 0 { rsa_key_text = hodu_rsa_key_text }

	pb, b = pem.Decode(rsa_key_text)
	if pb == nil || len(b) > 0 {
		return nil, fmt.Errorf("invalid token rsa key text %s - no block or too many blocks", string(rsa_key_text))
	}

	rk, err = x509.ParsePKCS1PrivateKey(pb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid token rsa key text %s - %s", string(rsa_key_text), err.Error())
	}

	config.TokenRsaKey = rk

	// load access rules
	config.AccessRules = make([]hodu.HttpAccessRule, len(cfg.AccessRules))
	for idx, rule = range cfg.AccessRules {
		var action hodu.HttpAccessAction
		var orgnet string
		var orgnet_idx int

		if rule.Prefix == "" {
			return nil, fmt.Errorf("blank access rule prefix not allowed")
		}

		switch strings.ToLower(rule.Action) {
			case "accept":
				action = hodu.HTTP_ACCESS_ACCEPT
			case "reject":
				action = hodu.HTTP_ACCESS_REJECT
			case "auth-required":
				action = hodu.HTTP_ACCESS_AUTH_REQUIRED
			default:
				return nil, fmt.Errorf("invalid access rule action %s", rule.Action)
		}

		config.AccessRules[idx] = hodu.HttpAccessRule{
			Prefix: rule.Prefix,
			Action: action,
			OrgNets: make([]netip.Prefix, len(rule.OrgNets)),
		}

		for orgnet_idx, orgnet = range rule.OrgNets {
			var netpfx netip.Prefix
			netpfx, err = netip.ParsePrefix(orgnet)
			if err != nil { return nil, fmt.Errorf("invalid network %s - %s", orgnet, err.Error()) }
			config.AccessRules[idx].OrgNets[orgnet_idx] = netpfx
		}
	}

	return &config, nil
}
