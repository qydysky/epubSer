package main

import (
	"archive/zip"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	pf "github.com/qydysky/part/file"
	ps "github.com/qydysky/part/slice"
	pweb "github.com/qydysky/part/web"
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
	Id   string `xml:"id,attr" json:"-"`
	Href string `xml:"href,attr" json:"-"`
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

	for epubf := range f.DirFilesRange(func(fi os.FileInfo) bool {
		return !strings.Contains(fi.Name(), base)
	}) {
		if rc, e := zip.OpenReader(epubf.Name()); e != nil {
			fmt.Println(e)
		} else if opfF, e := rc.Open("OEBPS/content.opf"); e != nil {
			fmt.Println(e)
		} else {
			var opf = Opf{
				BaseUrl: epubf.Name(),
			}
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
	}

	if data, e := json.Marshal(result); e != nil {
		fmt.Println(e)
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.Header().Set("Content-Type", "application/json")
		if _, e := w.Write(data); e != nil {
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
		if rc, e := zip.OpenReader(f.Name()); e != nil {
			fmt.Println(e)
		} else if opfF, e := rc.Open("OEBPS/content.opf"); e != nil {
			fmt.Println(e)
		} else {
			var opf = Opf{
				BaseUrl: f.Name(),
			}
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
				if data, e := json.Marshal(opf); e != nil {
					fmt.Println(e)
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					w.Header().Set("Content-Type", "application/json")
					if _, e := w.Write(data); e != nil {
						fmt.Println(e)
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}
			}
		}
	}
}

type Toc struct {
	Chapters []struct {
		BaseUrl string `json:"baseUrl,omitempty"`
		Title   string `xml:"navLabel>text" json:"title,omitempty"`
		Content struct {
			Url string `xml:"src,attr" json:"url,omitempty"`
		} `xml:"content" json:"content,omitempty"`
	} `xml:"navMap>navPoint" json:"chapters,omitempty"`
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

		if rc, e := zip.OpenReader(f.Name()); e != nil {
			fmt.Println(e)
		} else if opfF, e := rc.Open("OEBPS/toc.ncx"); e != nil {
			fmt.Println(e)
		} else {
			var toc Toc
			if e := xml.NewDecoder(opfF).Decode(&toc); e != nil {
				fmt.Println(e)
			} else {
				for baseUrl, i := f.Name(), 0; i < len(toc.Chapters); i++ {
					toc.Chapters[i].BaseUrl = baseUrl
				}
				if data, e := json.Marshal(toc); e != nil {
					fmt.Println(e)
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					w.Header().Set("Content-Type", "application/json")
					if _, e := w.Write(data); e != nil {
						fmt.Println(e)
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}
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
		if rc, e := zip.OpenReader(f.Name()); e != nil {
			fmt.Println(e)
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			if opfF, e := rc.Open("OEBPS" + content); e != nil {
				fmt.Println(e)
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				_, _ = io.Copy(w, opfF)
			}
		}
	}
}

func booksource(w http.ResponseWriter, r *http.Request) {
	if !pweb.MethodFiliter(w, r, http.MethodOptions, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(booksourceJson)
}

func parseBaseContent(method string, u *url.URL) (base, content string) {
	basecontent := strings.SplitAfterN(strings.TrimPrefix(u.Path, method), ".epub", 2)
	if len(basecontent) != 2 {
		return
	}

	return basecontent[0], basecontent[1]
}
