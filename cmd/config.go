package main

import "errors"
import "io"
import "os"

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

type ServerConfig struct {
	CTL struct {
		TLS ServerTLSConfig `yaml:"tls"`
	} `yaml:"ctl"`

	RPC struct {
		TLS ServerTLSConfig `yaml:"tls"`
	} `yaml:"rpc"`
}

type ClientConfig struct {
	CTL struct {
		TLS ServerTLSConfig `yaml:"tls"`
	} `yaml:"ctl"`
	RPC struct {
		TLS ClientTLSConfig `yaml:"tls"`
	} `yaml:"rpc"`
}


func LoadServerConfig(cfgfile string) (*ServerConfig, error) {
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

func LoadClientConfig(cfgfile string) (*ClientConfig, error) {
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
