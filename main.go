package main

//go:generate statik -f -src templates

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/nikandfor/cli/flag"
	"github.com/nikandfor/tlog"
	"github.com/pkg/errors"
	"github.com/rakyll/statik/fs"

	_ "github.com/nikandfor/openapi-gen/statik"
)

var (
	out      = flag.String("output,o", "", "file to write result to (default to stdout)")
	tmpl     = flag.String("template,t", "", "template to use (list to show list)")
	tmplHelp = flag.Bool("template-help,H", false, "template help")
	args     = flag.StringSlice("arg,a", nil, "template args")
	dumpSpec = flag.Bool("dump-spec", false, "dump parsed specifications and exit")
	debug    = flag.Bool("debug", false, "do something and exit")
)

func main() {
	flag.Parse()

	if *tmpl == "" {
		fmt.Printf("template is required\n")
		os.Exit(1)
	}

	if *tmpl == "list" {
		err := listTemplates()
		if err != nil {
			fmt.Printf("error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if flag.NArg() != 1 {
		fmt.Printf("one argument expected\n")
		usage()
		os.Exit(-1)
	}

	err := run()
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var argsmap map[string]string
	if len(*args) != 0 {
		argsmap = make(map[string]string)
		for _, a := range *args {
			s := strings.SplitN(a, "=", 2)
			if len(s) == 1 {
				argsmap[s[0]] = ""
			} else {
				argsmap[s[0]] = s[1]
			}
		}
	}

	var r io.Reader
	if n := flag.Arg(0); n == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(n)
		if err != nil {
			return errors.Wrap(err, "open specification")
		}
		defer f.Close()

		r = f
	}

	data, err := ioutil.ReadAll(r)
	if err != nil {
		return errors.Wrap(err, "read specification")
	}

	l := openapi3.NewSwaggerLoader()

	s, err := l.LoadSwaggerFromData(data)
	if err != nil {
		return errors.Wrap(err, "load specification")
	}

	if *debug {
		v := s.Paths.Find("/templates").Get.Responses["200"]
		tlog.Printf("%T: %v\n", v, v)
		return nil
	}

	if *dumpSpec {
		data, err := json.Marshal(s)
		if err != nil {
			return errors.Wrap(err, "dump to json")
		}
		fmt.Printf("%s\n", data)
		return nil
	}

	root := "root"
	t := template.New(root)

	t.Funcs(template.FuncMap{
		"title": strings.Title,
		"untitle": func(s string) string {
			return strings.ToLower(s[:1]) + s[1:]
		},
		"toupper": strings.ToUpper,
		"tolower": strings.ToLower,
		"append": func(vs ...interface{}) interface{} {
			if len(vs) == 0 {
				return nil
			}
			for len(vs) != 1 {
				vs[1] = reflect.AppendSlice(reflect.ValueOf(vs[0]), reflect.ValueOf(vs[1])).Interface()
				vs = vs[1:]
			}
			return vs[0]
		},
		"dict": func(vs ...interface{}) map[string]interface{} {
			if len(vs) == 0 {
				return nil
			}
			if len(vs)%2 != 0 {
				panic("odd len")
			}
			m := make(map[string]interface{})
			for i := 0; i < len(vs); i += 2 {
				m[vs[i].(string)] = vs[i+1]
			}
			return m
		},
		"basename": func(p string) string {
			return path.Base(p)
		},
		"type": func(v interface{}) string {
			if v == nil {
				return "nil"
			}
			return reflect.TypeOf(v).String()
		},
		"dump": func(v interface{}) string {
			return fmt.Sprintf("%T %+v", v, v)
		},
		"string":   func(v interface{}) string { return fmt.Sprintf("%s", v) },
		"replacen": strings.Replace,
		"replace":  strings.ReplaceAll,
		"CamelCase": func(s string) string {
			w := strings.Split(s, "_")
			for i := range w {
				w[i] = strings.Title(w[i])
				switch w[i] {
				case "Id":
					w[i] = "ID"
				case "Html":
					w[i] = "HTML"
				case "Http":
					w[i] = "HTTP"
				}
			}
			return strings.Join(w, "")
		},
	})

	if filepath.IsAbs(*tmpl) || strings.HasPrefix(*tmpl, "."+string(os.PathSeparator)) {
		files := strings.Split(*tmpl, ",")
		root = filepath.Base(files[0])
		t, err = t.ParseFiles(files...)
	} else {
		err = loadTemplate(t, *tmpl)
	}
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	var w io.Writer
	if *out == "" {
		w = os.Stdout
	} else {
		f, err := os.Create(*out)
		if err != nil {
			return errors.Wrap(err, "open output file")
		}
		defer f.Close()

		w = f
	}

	err = t.ExecuteTemplate(w, root, map[string]interface{}{
		"help":    *tmplHelp,
		"args":    argsmap,
		"swagger": s,
		"command": strings.Join(os.Args, " "),
	})
	if err != nil {
		return errors.Wrap(err, "execute")
	}

	return nil
}

func loadTemplate(t *template.Template, name string) error {
	fs, err := fs.New()
	if err != nil {
		return errors.Wrap(err, "open templates")
	}

	err = loadEmbedded(fs, t, "/"+name+".tmpl")
	if err != nil {
		return err
	}

	ext := filepath.Ext(name)[1:]

	dir, err := fs.Open("/" + ext)
	if err != nil {
		return errors.Wrap(err, "list templates")
	}

	for {
		files, err := dir.Readdir(100)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Wrap(err, "enum templates")
		}
		if len(files) == 0 {
			break
		}

		for _, f := range files {
			err = loadEmbedded(fs, t, path.Join("/", ext, f.Name()))
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func loadEmbedded(fs http.FileSystem, t *template.Template, name string) error {
	f, err := fs.Open(name)
	if err != nil {
		tlog.Printf("open %v", name)
		return errors.Wrap(err, "open template")
	}
	defer f.Close()

	data, err := ioutil.ReadAll(f)
	if err != nil {
		return errors.Wrap(err, "read template")
	}

	_, err = t.Parse(string(data))
	if err != nil {
		return errors.Wrap(err, "parse template")
	}

	return nil
}

func listTemplates() error {
	fs, err := fs.New()
	if err != nil {
		return errors.Wrap(err, "open templates")
	}

	dir, err := fs.Open("/")
	if err != nil {
		return errors.Wrap(err, "list templates")
	}

	for {
		files, err := dir.Readdir(100)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Wrap(err, "enum templates")
		}
		if len(files) == 0 {
			break
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			name = strings.TrimSuffix(name, filepath.Ext(name)) // .tmpl
			fmt.Printf("%v\n", name)
		}
	}

	return nil
}

func usage() {
	flag.Usage("", "[OPTIONS] <openapi.yml> - generate servers, clients from openapi specification")
}
