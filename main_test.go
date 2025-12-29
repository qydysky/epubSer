package main

import (
	"encoding/xml"
	"testing"

	pf "github.com/qydysky/part/file"
)

func Test1(t *testing.T) {
	type Opf struct {
		Title       string `xml:"metadata>title" json:"name"`
		Description string `xml:"metadata>description" json:"intro"`
		Creator     string `xml:"metadata>creator" json:"author"`
		Meta        []struct {
			Name string `xml:"name,attr" json:"name"`
		} `xml:"metadata>meta" json:"meta"`
	}
	var opf Opf
	e := xml.NewDecoder(pf.Open("test.xml")).Decode(&opf)
	t.Log(e)
	t.Log(opf)
}
