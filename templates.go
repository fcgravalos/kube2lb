/*
Copyright 2016 Tuenti Technologies S.L. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"text/template"
)

var defaultServerNameTemplate = "{{ .Service.Name }}.{{ .Service.Namespace }}.svc.{{ .Domain }}"
var serverNameTemplatesArg string
var serverNameTemplates []*template.Template

func init() {
	flag.StringVar(&serverNameTemplatesArg, "server-name-templates", defaultServerNameTemplate, "Comma-separated list of go templates to generate server names")
}

type serverName string

func (s serverName) IsRegexp() bool {
	return strings.HasPrefix(string(s), "~")
}

func (s serverName) Regexp() string {
	return strings.TrimPrefix(string(s), "~")
}

func parseServerNameTemplatesArg(templatesArg string) ([]*template.Template, error) {
	if len(templatesArg) == 0 {
		templatesArg = defaultServerNameTemplate
	}
	templateStrings := strings.Split(templatesArg, ",")
	templates := make([]*template.Template, len(templateStrings))
	for i, templateString := range templateStrings {
		t, err := template.New("server_name").Parse(templateString)
		if err != nil {
			return nil, err
		}
		templates[i] = t
	}
	return templates, nil
}

func initServerNameTemplates() (err error) {
	if len(serverNameTemplates) > 0 {
		return nil
	}
	serverNameTemplates, err = parseServerNameTemplatesArg(serverNameTemplatesArg)
	return err
}

type PortSpec struct {
	IP       net.IP
	Port     int32
	Mode     string
	Protocol string
}

// String representation of a PortSpec, intended to be used as config label
func (s PortSpec) String() string {
	var encodedIP string
	if s.IP.To4() != nil {
		// No need to use more than 4 bytes if it is an IPv4 address
		encodedIP = hex.EncodeToString(s.IP.To4())
	} else {
		encodedIP = hex.EncodeToString(s.IP)
	}
	return fmt.Sprintf("%s_%d_%s_%s", encodedIP, s.Port, s.Protocol, s.Mode)
}

type ServiceInformation struct {
	Name      string
	Namespace string
	Port      PortSpec
	Endpoints []ServiceEndpoint
	NodePort  int32
	External  []string
	Timeout   int
}

// String representation of a Service, intended to be used as config label
func (s ServiceInformation) String() string {
	return fmt.Sprintf("%s_%s_%d_%s_%s",
		s.Name, s.Namespace, s.Port.Port, s.Port.Protocol, s.Port.Mode)
}

type ClusterInformation struct {
	Services []ServiceInformation
	Ports    []PortSpec
	Nodes    []string
	Domain   string
}

type Template interface {
	Execute(info *ClusterInformation) error
}

type templateFile struct {
	Source, Path string
}

func NewTemplate(source, path string) Template {
	return &templateFile{
		Source: source,
		Path:   path,
	}
}

func removeDuplicated(names []string) []string {
	seen := make(map[string]interface{})
	for _, name := range names {
		seen[name] = nil
	}
	uniq := make([]string, 0, len(seen))
	for k := range seen {
		uniq = append(uniq, k)
	}
	return uniq
}

func generateServerNames(s ServiceInformation, domain string) []serverName {
	serverNames := make([]string, len(serverNameTemplates))
	for i, t := range serverNameTemplates {
		data := struct {
			Service ServiceInformation
			Domain  string
		}{s, domain}
		var serverName bytes.Buffer
		t.Execute(&serverName, data)
		serverNames[i] = serverName.String()
	}
	return func() []serverName {
		var sns []serverName
		for _, n := range append(removeDuplicated(serverNames), s.External...) {
			sns = append(sns, serverName(n))
		}
		return sns
	}()
}

var nodeNameReplacer = strings.NewReplacer(".", "_", ":", "_")

func intRange(n, initial, step int) chan int {
	c := make(chan int)
	go func() {
		for i := 0; i < n; i++ {
			c <- initial + i*step
		}
		close(c)
	}()
	return c
}

func opAdd(ns ...int) int {
	r := 0
	for _, n := range ns {
		r += n
	}
	return r
}

func (t *templateFile) Execute(info *ClusterInformation) error {
	funcMap := template.FuncMap{
		"EscapeNode":  nodeNameReplacer.Replace,
		"IntRange":    intRange,
		"ServerNames": generateServerNames,
		"ToLower":     strings.ToLower,
		"ToUpper":     strings.ToUpper,
		"Add":         opAdd,
	}

	// template.Execute will use the base name of t.Source
	s, err := template.New(path.Base(t.Source)).Funcs(funcMap).ParseFiles(t.Source)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(t.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if err = s.Execute(f, info); err != nil {
		return err
	}
	return nil
}
