package main

import "crypto/tls"
import "crypto/x509"
import "encoding/base64"
import "errors"
import "fmt"
import "hodu"
import "io"
import "io/ioutil"
import "os"
import "strings"
import "time"

import "gopkg.in/yaml.v3"

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

type AuthConfig struct {
	Enabled bool `yaml:"enabled"`
	Realm string `yaml:"realm"`
	Creds []string `yaml:"credentials"`
	TokenTtl string `yaml:"token-ttl"`
}

type CTLServiceConfig struct {
	Prefix string   `yaml:"prefix"`  // url prefix for control channel endpoints
	Addrs  []string `yaml:"addresses"`
	Auth   AuthConfig `yaml:"auth"`
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
}

type ServerConfig struct {
	APP ServerAppConfig             `yaml:"app"`

	CTL struct {
		Service CTLServiceConfig    `yaml:"service"`
		TLS ServerTLSConfig         `yaml:"tls"`
	} `yaml:"ctl"`

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
}

func load_server_config(cfgfile string) (*ServerConfig, error) {
	var cfg ServerConfig
	var f *os.File
	var yd *yaml.Decoder
	var err error

	f, err = os.Open(cfgfile)
	if err != nil && errors.Is(err, io.EOF) {
		return nil, err
	}

	yd = yaml.NewDecoder(f)
	err = yd.Decode(&cfg)
	f.Close()
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}

func load_client_config(cfgfile string) (*ClientConfig, error) {
	var cfg ClientConfig
	var f *os.File
	var yd *yaml.Decoder
	var err error

	f, err = os.Open(cfgfile)
	if err != nil && errors.Is(err, io.EOF) {
		return nil, err
	}

	yd = yaml.NewDecoder(f)
	err = yd.Decode(&cfg)
	f.Close()
	if err != nil {
		return nil, err
	}

	return &cfg, nil
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
			return nil, fmt.Errorf("failed to load key pair - %s", err)
		}

		cert_pool = x509.NewCertPool()
		if cfg.ClientCACertText != "" {
			ok = cert_pool.AppendCertsFromPEM([]byte(cfg.ClientCACertText))
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else if cfg.ClientCACertFile != "" {
			var text []byte
			text, err = ioutil.ReadFile(cfg.ClientCACertFile)
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
			return nil, fmt.Errorf("failed to load key pair - %s", err)
		}

		cert_pool = x509.NewCertPool()
		if cfg.ServerCACertText != "" {
			ok = cert_pool.AppendCertsFromPEM([]byte(cfg.ServerCACertText))
			if !ok {
				return nil, fmt.Errorf("failed to append certificate to pool")
			}
		} else if cfg.ServerCACertFile != "" {
			var text []byte
			text, err = ioutil.ReadFile(cfg.ServerCACertFile)
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
func make_server_basic_auth_config(cfg *AuthConfig) (*hodu.ServerAuthConfig, error) {
	var config hodu.ServerAuthConfig
	var cred string
	var b []byte
	var x []string
	var err error

	config.Enabled = cfg.Enabled
	config.Realm = cfg.Realm
	config.Creds = make(hodu.ServerAuthCredMap)
	config.TokenTtl, err = hodu.ParseDurationString(cfg.TokenTtl)
	if err != nil {
		return nil, fmt.Errorf("invalid token ttl %s - %s", cred, err)
	}

	for _, cred = range cfg.Creds {
		b, err = base64.StdEncoding.DecodeString(cred)
		if err == nil { cred = string(b) }

		// each entry must be of the form username:password
		x = strings.Split(cred, ":")
		if len(x) != 2 {
			return nil, fmt.Errorf("invalid basic auth credential - %s", cred)
		}

		config.Creds[x[0]] = x[1]
	}

	return &config, nil
}
