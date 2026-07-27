package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dustin/go-humanize"
	pf "github.com/qydysky/part/file"
	ps "github.com/qydysky/part/slice"
	pweb "github.com/qydysky/part/web"
	pxml "github.com/qydysky/part/xml"
)

var (
	//go:embed index.html
	indexHtml []byte
	//go:embed booksource.json
	booksourceJson []byte
)

var (
	addrP = flag.String("addr", "0.0.0.0:10005", "addr")
	dirP  = flag.String("dir", "./", "epub dir")
)

func main() {
	flag.Parse()

	if !strings.HasSuffix(*dirP, "/") {
		fmt.Println("dir必须以/结尾")
	}

	webPath := pweb.WebPath{}
	if web, e := pweb.NewSyncMapNoPanic(&http.Server{
		Addr: *addrP,
	}, &webPath, webPath.LoadPerfix); e != nil {
		fmt.Println(e)
	} else {
		defer web.Shutdown()
	}

	webPath.Store(`/`, index)
	webPath.Store(`/search/`, search)
	webPath.Store(`/info/`, info)
	webPath.Store(`/chapter/`, chapter)
	webPath.Store(`/content/`, content)
	webPath.Store(`/booksource`, booksource)

	fmt.Println("epub 阅读服务")
	fmt.Println("启动于", *addrP)
	fmt.Println("服务目录", *dirP)

	//ctrl+c退出
	var interrupt = make(chan os.Signal, 2)
	//捕获ctrl+c、容器退出
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	fmt.Println("ctrl+c退出")
	<-interrupt
}

func index(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}
	_, _ = w.Write(indexHtml)
}

type Meta struct {
	Name    string `xml:"name,attr" json:"-"`
	Content string `xml:"content,attr" json:"-"`
}
type Manifest struct {
	Id        string `xml:"id,attr" json:"-"`
	Href      string `xml:"href,attr" json:"-"`
	MediaType string `xml:"media-type,attr" json:"-"`
}
type Opf struct {
	BaseUrl     string     `json:"baseUrl,omitempty"`
	CoverUrl    string     `json:"coverUrl,omitempty"`
	Title       string     `xml:"metadata>title" json:"name,omitempty"`
	Description string     `xml:"metadata>description" json:"intro,omitempty"`
	Creator     string     `xml:"metadata>creator" json:"author,omitempty"`
	Meta        []Meta     `xml:"metadata>meta" json:"-"`
	Manifest    []Manifest `xml:"manifest>item" json:"-"`
}

func search(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}

	base := strings.TrimPrefix(r.URL.Path, "/search/")
	if base == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f := pf.Open(*dirP).CheckRoot(*dirP)

	var result struct {
		List []Opf `json:"list"`
	}
	result.List = []Opf{}

	for file := range f.DirFilesRange(func(fi os.FileInfo) bool {
		return !strings.Contains(fi.Name(), base)
	}) {
		var opf = Opf{
			BaseUrl: file.SelfName(),
		}
		switch ext := strings.ToUpper(filepath.Ext(file.SelfName())); ext {
		case `.TXT`:
			f := pf.Open(file.Name())
			var buf []byte
			if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
				opf.Title = string(buf)
			}
			if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
				opf.Creator = string(buf)
			}
			for i := 0; i < 50; i++ {
				if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
					opf.Description += string(buf)
				}
				if len(buf) == 0 {
					break
				}
			}
			result.List = append(result.List, opf)
		case `.EPUB`:
			if rc, e := zip.OpenReader(file.Name()); e != nil {
				fmt.Println(e)
			} else if opfF, e := rc.Open("OEBPS/content.opf"); e != nil {
				fmt.Println(e)
			} else {
				if e := xml.NewDecoder(opfF).Decode(&opf); e != nil {
					fmt.Println(e)
				} else {
					if _, coverMeta := ps.Search(opf.Meta, func(t *Meta) bool {
						return t.Name == "cover"
					}); coverMeta != nil {
						if _, coverManifest := ps.Search(opf.Manifest, func(t *Manifest) bool {
							return t.Id == coverMeta.Content
						}); coverManifest != nil {
							opf.CoverUrl = coverManifest.Href
						}
					}
					result.List = append(result.List, opf)
				}
			}
		default:
			fmt.Println(ext)
		}
	}

	if data, e := json.Marshal(result); e != nil {
		fmt.Println(e)
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.Header().Set("Content-Type", "application/json")
		gzipw, cf := gzipEncode(w, r)
		defer cf()
		if _, e := gzipw.Write(data); e != nil {
			fmt.Println(e)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
}

func info(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}

	base, _ := parseBaseContent("/info/", r.URL)
	if base == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f := pf.Open(*dirP).CheckRoot(*dirP).Open(base)

	if !f.IsExist() {
		w.WriteHeader(http.StatusNotFound)
	} else {
		var opf = Opf{
			BaseUrl: f.SelfName(),
		}
		switch ext := strings.ToUpper(filepath.Ext(f.SelfName())); ext {
		case `.TXT`:
			var buf []byte
			if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
				opf.Title = string(buf)
			}
			if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
				opf.Creator = string(buf)
			}
			for i := 0; i < 50; i++ {
				if e := f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.KByte); e == nil {
					opf.Description += string(buf)
				}
				if len(buf) == 0 {
					break
				}
			}
		case `.EPUB`:
			if rc, e := zip.OpenReader(f.Name()); e != nil {
				fmt.Println(e)
			} else if opfF, e := rc.Open("OEBPS/content.opf"); e != nil {
				fmt.Println(e)
			} else {
				if e := xml.NewDecoder(opfF).Decode(&opf); e != nil {
					fmt.Println(e)
				} else {
					if _, coverMeta := ps.Search(opf.Meta, func(t *Meta) bool {
						return t.Name == "cover"
					}); coverMeta != nil {
						if _, coverManifest := ps.Search(opf.Manifest, func(t *Manifest) bool {
							return t.Id == coverMeta.Content
						}); coverManifest != nil {
							opf.CoverUrl = coverManifest.Href
						}
					}
				}
			}
		default:
			fmt.Println(ext)
		}
		if data, e := json.Marshal(opf); e != nil {
			fmt.Println(e)
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.Header().Set("Content-Type", "application/json")
			gzipw, cf := gzipEncode(w, r)
			defer cf()
			if _, e := gzipw.Write(data); e != nil {
				fmt.Println(e)
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
	}
}

type Toc struct {
	Chapters []Chapters `xml:"navMap>navPoint" json:"chapters,omitempty"`
}

type Chapters struct {
	BaseUrl string  `json:"baseUrl,omitempty"`
	Title   string  `xml:"navLabel>text" json:"title,omitempty"`
	Content Content `xml:"content" json:"content,omitempty"`
}

type Content struct {
	Url string `xml:"src,attr" json:"url,omitempty"`
}

func chapter(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}

	base, _ := parseBaseContent("/chapter/", r.URL)
	if base == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f := pf.Open(*dirP).CheckRoot(*dirP).Open(base)

	if !f.IsExist() {
		w.WriteHeader(http.StatusNotFound)
	} else {
		baseName := f.SelfName()
		var toc Toc
		switch ext := strings.ToUpper(filepath.Ext(f.SelfName())); ext {
		case `.TXT`:
			var (
				buf []byte
				e   error
			)
			for e == nil {
				for e == nil {
					if e = f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.MByte); e != nil {
						break
					}
					if len(buf) == 0 {
						break
					}
				}
				cui, _ := f.CurIndex()
				if e = f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.MByte); e != nil {
					break
				}
				toc.Chapters = append(toc.Chapters, Chapters{
					BaseUrl: baseName,
					Content: Content{fmt.Sprintf("Text/%d", cui)},
					Title:   string(buf),
				})
			}
		case `.EPUB`:
			if rc, e := zip.OpenReader(f.Name()); e != nil {
				fmt.Println(e)
			} else if opfF, e := rc.Open("OEBPS/toc.ncx"); e != nil {
				fmt.Println(e)
			} else {
				if e := xml.NewDecoder(opfF).Decode(&toc); e != nil {
					fmt.Println(e)
				} else {
					for i := 0; i < len(toc.Chapters); i++ {
						toc.Chapters[i].BaseUrl = baseName
					}
				}
			}
		default:
		}
		if data, e := json.Marshal(toc); e != nil {
			fmt.Println(e)
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.Header().Set("Content-Type", "application/json")
			gzipw, cf := gzipEncode(w, r)
			defer cf()
			if _, e := gzipw.Write(data); e != nil {
				fmt.Println(e)
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}
	}
}

func content(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}

	base, content := parseBaseContent("/content/", r.URL)
	if base == "" || content == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f := pf.Open(*dirP).CheckRoot(*dirP).Open(base)

	if !f.IsExist() {
		w.WriteHeader(http.StatusNotFound)
	} else {

		switch ext := strings.ToUpper(filepath.Ext(f.SelfName())); ext {
		case `.TXT`:
			if l := strings.Split(content, "/"); len(l) != 2 || l[0] != "Text" {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else if index, e := strconv.Atoi(l[1]); e != nil {
				fmt.Println(e)
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				gzipw, cf := gzipEncode(w, r)
				defer cf()

				format := r.URL.Query().Get("format")
				if format == "json" {
					w.Header().Set("Content-Type", "application/json")
				} else {
					w.Header().Set("Content-Type", "application/xhtml+xml")
				}

				var (
					buf []byte
					e   error
				)

				var jsonC struct {
					Header string `json:"header"`
					Body   string `json:"body"`
				}

				if e = f.SeekIndex(int64(index), pf.AtOrigin); e != nil {
					return
				}
				if e = f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.MByte); e != nil {
					return
				}

				if format == `json` {
					jsonC.Header = string(buf)
					defer func() {
						if data, e := json.Marshal(jsonC); e == nil {
							_, _ = gzipw.Write(data)
						}
					}()
				} else {
					_, _ = gzipw.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>`))
					_, _ = gzipw.Write([]byte(`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">`))
					_, _ = gzipw.Write([]byte(`<html xmlns="http://www.w3.org/1999/xhtml">`))
					defer gzipw.Write([]byte(`</html>`))
					_, _ = gzipw.Write([]byte(`<body>`))
					defer gzipw.Write([]byte(`</body>`))

					_, _ = gzipw.Write([]byte(`<h2 class="head">`))
					_, _ = gzipw.Write(buf)
					_, _ = gzipw.Write([]byte(`</h2>`))
				}

				for e == nil {
					if e = f.ReadUntilV2(&buf, []byte{'\n'}, humanize.KByte, humanize.MByte); e != nil {
						return
					}
					if len(buf) == 0 {
						break
					}

					if format == `json` {
						jsonC.Body += string(buf)
					} else {
						_, _ = gzipw.Write([]byte(`<p>`))
						_, _ = gzipw.Write(buf)
						_, _ = gzipw.Write([]byte(`</p>`))
					}
				}
			}
		case `.EPUB`:
			if rc, e := zip.OpenReader(f.Name()); e != nil {
				fmt.Println(e)
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				if c, e := rc.Open("OEBPS/" + content); e != nil {
					fmt.Println(e)
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					gzipw, cf := gzipEncode(w, r)
					defer cf()

					if opfF, e := rc.Open("OEBPS/content.opf"); e == nil {
						var opf = Opf{}
						if e := xml.NewDecoder(opfF).Decode(&opf); e == nil {
							if _, coverManifest := ps.Search(opf.Manifest, func(t *Manifest) bool {
								return t.Href == content
							}); coverManifest != nil {
								if format := r.URL.Query().Get("format"); format == "json" && coverManifest.MediaType == "application/xhtml+xml" {
									xmlf := pxml.NewDecoder(c)
									var jsonC struct {
										Header string `json:"header"`
										Body   string `json:"body"`
									}
									jsonC.Header, jsonC.Body = getJsonContent(xmlf)
									if data, e := json.Marshal(jsonC); e == nil {
										w.Header().Set("Content-Type", "application/json")
										_, _ = gzipw.Write(data)
									}
								} else {
									w.Header().Set("Content-Type", coverManifest.MediaType)
									_, _ = io.Copy(gzipw, c)
								}
							}
						}
					}

				}
			}
		default:
		}
	}
}

func booksource(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	gzipw, cf := gzipEncode(w, r)
	defer cf()
	_, _ = gzipw.Write(booksourceJson)
}

func parseBaseContent(method string, u *url.URL) (base, content string) {
	basecontent := strings.SplitN(strings.TrimPrefix(u.Path, method), "/", 2)
	if len(basecontent) > 0 {
		base = basecontent[0]
	}
	if len(basecontent) > 1 {
		content = basecontent[1]
	}
	return
}

func gzipEncode(w http.ResponseWriter, r *http.Request) (wf io.Writer, cf func() error) {
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gw := gzip.NewWriter(w)
		return gw, gw.Close
	}
	return w, func() error { return nil }
}

func getJsonContent(i iter.Seq[*pxml.Node]) (header, body string) {
	var title byte
	var bodyA bool
	for line := range i {
		if len(line.Name) == 3 && line.Name[0] == '/' && line.Name[1] == 'h' && line.Name[2] == title {
			title = 0
		}
		if len(line.Name) == 2 && line.Name[0] == 'h' && line.Name[1] >= '1' && line.Name[1] <= '9' {
			title = line.Name[1]
		}
		if title != 0 {
			if b := bytes.TrimSpace(line.Inner); len(b) == 0 {
				if len(header) != 0 && !strings.HasSuffix(header, " ") {
					header += " "
				}
			} else {
				header += string(b)
			}
		}
		if bytes.Equal(line.Name, []byte("/p")) || bytes.Equal(line.Name, []byte("/div")) {
			bodyA = false
		}
		if bytes.Equal(line.Name, []byte("p")) || bytes.Equal(line.Name, []byte("div")) {
			bodyA = true
		}
		if bodyA && len(bytes.TrimSpace(line.Inner)) > 0 {
			body += "\t" + string(line.Inner) + "\n"
		}
	}
	return
}
