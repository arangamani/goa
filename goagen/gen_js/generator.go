package genjs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/raphael/goa/design"
	"github.com/raphael/goa/goagen/codegen"
	"github.com/raphael/goa/goagen/utils"

	"gopkg.in/alecthomas/kingpin.v2"
)

// Generator is the application code generator.
type Generator struct {
	genfiles []string
}

// Generate is the generator entry point called by the meta generator.
func Generate(api *design.APIDefinition) ([]string, error) {
	g, err := NewGenerator()
	if err != nil {
		return nil, err
	}
	return g.Generate(api)
}

// NewGenerator returns the application code generator.
func NewGenerator() (*Generator, error) {
	app := kingpin.New("JavaScript generator", "JavaScript client module")
	codegen.RegisterFlags(app)
	NewCommand().RegisterFlags(app)
	_, err := app.Parse(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf(`invalid command line: %s. Command line was "%s"`,
			err, strings.Join(os.Args, " "))
	}
	return new(Generator), nil
}

// Generate produces the skeleton main.
func (g *Generator) Generate(api *design.APIDefinition) (_ []string, err error) {
	go utils.Catch(nil, func() { g.Cleanup() })

	defer func() {
		if err != nil {
			g.Cleanup()
		}
	}()

	if Host == "" {
		Host = api.Host
	}
	if Host == "" {
		return nil, fmt.Errorf("missing host value, specify it with --host")
	}
	codegen.OutputDir = filepath.Join(codegen.OutputDir, "js")
	if err = os.RemoveAll(codegen.OutputDir); err != nil {
		return
	}
	if err = os.MkdirAll(codegen.OutputDir, 0755); err != nil {
		return
	}
	g.genfiles = append(g.genfiles, codegen.OutputDir)
	funcs := template.FuncMap{
		"title":   strings.Title,
		"join":    strings.Join,
		"toLower": strings.ToLower,
		"params":  params,
	}
	filePath := filepath.Join(codegen.OutputDir, "client.js")
	tmpl, err := template.New("module").Funcs(funcs).Parse(moduleT)
	if err != nil {
		panic(err.Error()) // bug
	}
	g.genfiles = append(g.genfiles, filePath)
	if Scheme == "" && len(api.Schemes) > 0 {
		Scheme = api.Schemes[0]
	}
	data := map[string]interface{}{
		"API":     api,
		"Host":    Host,
		"Scheme":  Scheme,
		"Timeout": int64(Timeout / time.Millisecond),
	}
	var file *os.File
	if file, err = os.Create(filePath); err != nil {
		return
	}
	if err = tmpl.Execute(file, data); err != nil {
		return
	}
	actions := make(map[string][]*design.ActionDefinition)
	api.IterateResources(func(res *design.ResourceDefinition) error {
		return res.IterateActions(func(action *design.ActionDefinition) error {
			if as, ok := actions[action.Name]; ok {
				actions[action.Name] = append(as, action)
			} else {
				actions[action.Name] = []*design.ActionDefinition{action}
			}
			return nil
		})
	})
	if tmpl, err = template.New("jsFuncs").Funcs(funcs).Parse(jsFuncsT); err != nil {
		panic(err.Error()) // bug
	}
	var exampleAction *design.ActionDefinition
	keys := make([]string, len(actions))
	i := 0
	for n := range actions {
		keys[i] = n
		i++
	}
	sort.Strings(keys)
	for _, n := range keys {
		as, _ := actions[n]
		for _, a := range as {
			if exampleAction == nil && a.Routes[0].Verb == "GET" {
				exampleAction = a
			}
			if err = tmpl.Execute(file, a); err != nil {
				return
			}
		}
	}
	file.Write([]byte(moduleTend))
	file.Close()

	if exampleAction != nil {
		filePath = filepath.Join(codegen.OutputDir, "index.html")
		if file, err = os.Create(filePath); err != nil {
			return
		}
		if tmpl, err = template.New("exampleHTML").Funcs(funcs).Parse(exampleT); err != nil {
			panic(err.Error()) // bug
		}
		g.genfiles = append(g.genfiles, filePath)
		argNames := params(exampleAction)
		var args string
		if len(argNames) > 0 {
			query := exampleAction.QueryParams.Type.ToObject()
			argValues := make([]string, len(argNames))
			for i, n := range argNames {
				q := query[n].Type.ToArray().ElemType
				// below works because we deal with simple types in query strings
				argValues[i] = fmt.Sprintf("%v", api.Example(q.Type))
			}
			args = strings.Join(argValues, ", ")
		}
		examplePath := exampleAction.Routes[0].FullPath()
		pathParams := exampleAction.Routes[0].Params()
		if len(pathParams) > 0 {
			pathVars := exampleAction.AllParams().Type.ToObject()
			pathValues := make([]interface{}, len(pathParams))
			for i, n := range pathParams {
				pathValues[i] = api.Example(pathVars[n].Type)
			}
			format := design.WildcardRegex.ReplaceAllLiteralString(examplePath, "/%v")
			examplePath = fmt.Sprintf(format, pathValues...)
		}
		if len(argNames) > 0 {
			args = ", " + args
		}
		exampleFunc := fmt.Sprintf(
			`%s%s ("%s"%s)`,
			exampleAction.Name,
			strings.Title(exampleAction.Parent.Name),
			examplePath,
			args,
		)
		err = tmpl.Execute(file, map[string]interface{}{"API": api, "ExampleFunc": exampleFunc})
		file.Close()

		filePath = filepath.Join(codegen.OutputDir, "axios.min.js")
		if file, err = os.Create(filePath); err != nil {
			return
		}
		g.genfiles = append(g.genfiles, filePath)
		file.Write([]byte(axios))
		file.Close()

		controllerFile := filepath.Join(codegen.OutputDir, "example.go")
		if tmpl, err = template.New("exampleController").Parse(exampleCtrlT); err != nil {
			panic(err.Error()) // bug
		}
		gg := codegen.NewGoGenerator(controllerFile)
		imports := []*codegen.ImportSpec{
			codegen.SimpleImport("net/http"),
			codegen.SimpleImport("github.com/julienschmidt/httprouter"),
			codegen.SimpleImport("github.com/raphael/goa"),
		}
		g.genfiles = append(g.genfiles, controllerFile)
		gg.WriteHeader(fmt.Sprintf("%s JavaScript Client Example", api.Name), "js", imports)
		data := map[string]interface{}{
			"ServeDir": codegen.OutputDir,
		}
		if err = tmpl.Execute(gg, data); err != nil {
			return
		}
		if err = gg.FormatCode(); err != nil {
			return
		}
	}

	return g.genfiles, nil
}

// Cleanup removes all the files generated by this generator during the last invokation of Generate.
func (g *Generator) Cleanup() {
	for _, f := range g.genfiles {
		os.Remove(f)
	}
	g.genfiles = nil
}

func params(action *design.ActionDefinition) []string {
	if action.QueryParams == nil {
		return nil
	}
	params := make([]string, len(action.QueryParams.Type.ToObject()))
	i := 0
	for n := range action.QueryParams.Type.ToObject() {
		params[i] = n
		i++
	}
	sort.Strings(params)
	return params
}

const moduleT = `// This module exports functions that give access to the {{.API.Name}} API hosted at {{.API.Host}}.
// It uses the axios javascript library for making the actual HTTP requests.
define(['axios'] , function (axios) {
  function merge(obj1, obj2) {
    var obj3 = {};
    for (var attrname in obj1) { obj3[attrname] = obj1[attrname]; }
    for (var attrname in obj2) { obj3[attrname] = obj2[attrname]; }
    return obj3;
  }

  return function (scheme, host, timeout) {
    scheme = scheme || '{{.Scheme}}';
    host = host || '{{.Host}}';
    timeout = timeout || {{.Timeout}};

    // Client is the object returned by this module.
    var client = axios;

    // URL prefix for all API requests.
    var urlPrefix = scheme + '://' + host;
`

const moduleTend = `  return client;
  };
});
`

const jsFuncsT = `{{$params := params .}}
  {{$name := printf "%s%s" .Name (title .Parent.Name)}}// {{if .Description}}{{.Description}}{{else}}{{$name}} calls the {{.Name}} action of the {{.Parent.Name}} resource.{{end}}
  // path is the request path, the format is "{{(index .Routes 0).FullPath}}"
  {{if .Payload}}// data contains the action payload (request body)
  {{end}}{{if $params}}// {{join $params ", "}} {{if gt (len $params) 1}}are{{else}}is{{end}} used to build the request query string.
  {{end}}// config is an optional object to be merged into the config built by the function prior to making the request.
  // The content of the config object is described here: https://github.com/mzabriskie/axios#request-api
  // This function returns a promise which raises an error if the HTTP response is a 4xx or 5xx.
  client.{{$name}} = function (path{{if .Payload}}, data{{end}}{{if $params}}, {{join $params ", "}}{{end}}, config) {
    cfg = {
      timeout: timeout,
      url: urlPrefix + path,
      method: '{{toLower (index .Routes 0).Verb}}',
{{if $params}}      params: {
{{range $index, $param := $params}}{{if $index}},
{{end}}        {{$param}}: {{$param}}{{end}}
      },
{{end}}{{if .Payload}}    data: data,
{{end}}      responseType: 'json'
    };
    if (config) {
      cfg = merge(cfg, config);
    }
    return client(cfg);
  }
`

const exampleT = `<!doctype html>
<html>
  <head>
    <title>goa JavaScript client loader</title>
  </head>
  <body>
    <h1>{{.API.Name}} Client Test</h1>
    <div id="response"></div>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/require.js/2.1.16/require.min.js"></script>
    <script>
      requirejs.config({
        paths: {
          axios: '/js/axios.min',
          client: '/js/client'
        }
      });
      requirejs(['client'], function (client) {
        client().{{.ExampleFunc}}
          .then(function (resp) {
            document.getElementById('response').innerHTML = resp.statusText;
          })
          .catch(function (resp) {
            document.getElementById('response').innerHTML = resp.statusText;
          });
      });
    </script>
  </body>
</html>
`

const exampleCtrlT = `// MountController mounts the JavaScript example controller under "/js".
func MountController(service goa.Service) {
	// Serve static files under js
	service.ServeFiles("/js/*filepath", http.Dir("{{.ServeDir}}"))
	service.Info("mount", "ctrl", "JS", "action", "ServeFiles", "route", "GET /js/*")
}
`

const axios = `/* axios v0.7.0 | (c) 2015 by Matt Zabriskie */
!function(e,t){"object"==typeof exports&&"object"==typeof module?module.exports=t():"function"==typeof define&&define.amd?define([],t):"object"==typeof exports?exports.axios=t():e.axios=t()}(this,function(){return function(e){function t(r){if(n[r])return n[r].exports;var o=n[r]={exports:{},id:r,loaded:!1};return e[r].call(o.exports,o,o.exports,t),o.loaded=!0,o.exports}var n={};return t.m=e,t.c=n,t.p="",t(0)}([function(e,t,n){e.exports=n(1)},function(e,t,n){"use strict";var r=n(2),o=n(3),i=n(4),s=n(12),u=e.exports=function(e){"string"==typeof e&&(e=o.merge({url:arguments[0]},arguments[1])),e=o.merge({method:"get",headers:{},timeout:r.timeout,transformRequest:r.transformRequest,transformResponse:r.transformResponse},e),e.withCredentials=e.withCredentials||r.withCredentials;var t=[i,void 0],n=Promise.resolve(e);for(u.interceptors.request.forEach(function(e){t.unshift(e.fulfilled,e.rejected)}),u.interceptors.response.forEach(function(e){t.push(e.fulfilled,e.rejected)});t.length;)n=n.then(t.shift(),t.shift());return n};u.defaults=r,u.all=function(e){return Promise.all(e)},u.spread=n(13),u.interceptors={request:new s,response:new s},function(){function e(){o.forEach(arguments,function(e){u[e]=function(t,n){return u(o.merge(n||{},{method:e,url:t}))}})}function t(){o.forEach(arguments,function(e){u[e]=function(t,n,r){return u(o.merge(r||{},{method:e,url:t,data:n}))}})}e("delete","get","head"),t("post","put","patch")}()},function(e,t,n){"use strict";var r=n(3),o=/^\)\]\}',?\n/,i={"Content-Type":"application/x-www-form-urlencoded"};e.exports={transformRequest:[function(e,t){return r.isFormData(e)?e:r.isArrayBuffer(e)?e:r.isArrayBufferView(e)?e.buffer:!r.isObject(e)||r.isFile(e)||r.isBlob(e)?e:(r.isUndefined(t)||(r.forEach(t,function(e,n){"content-type"===n.toLowerCase()&&(t["Content-Type"]=e)}),r.isUndefined(t["Content-Type"])&&(t["Content-Type"]="application/json;charset=utf-8")),JSON.stringify(e))}],transformResponse:[function(e){if("string"==typeof e){e=e.replace(o,"");try{e=JSON.parse(e)}catch(t){}}return e}],headers:{common:{Accept:"application/json, text/plain, */*"},patch:r.merge(i),post:r.merge(i),put:r.merge(i)},timeout:0,xsrfCookieName:"XSRF-TOKEN",xsrfHeaderName:"X-XSRF-TOKEN"}},function(e,t){"use strict";function n(e){return"[object Array]"===v.call(e)}function r(e){return"[object ArrayBuffer]"===v.call(e)}function o(e){return"[object FormData]"===v.call(e)}function i(e){return"undefined"!=typeof ArrayBuffer&&ArrayBuffer.isView?ArrayBuffer.isView(e):e&&e.buffer&&e.buffer instanceof ArrayBuffer}function s(e){return"string"==typeof e}function u(e){return"number"==typeof e}function a(e){return"undefined"==typeof e}function f(e){return null!==e&&"object"==typeof e}function c(e){return"[object Date]"===v.call(e)}function p(e){return"[object File]"===v.call(e)}function l(e){return"[object Blob]"===v.call(e)}function d(e){return e.replace(/^\s*/,"").replace(/\s*$/,"")}function h(e){return"[object Arguments]"===v.call(e)}function m(){return"undefined"!=typeof window&&"undefined"!=typeof document&&"function"==typeof document.createElement}function y(e,t){if(null!==e&&"undefined"!=typeof e){var r=n(e)||h(e);if("object"==typeof e||r||(e=[e]),r)for(var o=0,i=e.length;i>o;o++)t.call(null,e[o],o,e);else for(var s in e)e.hasOwnProperty(s)&&t.call(null,e[s],s,e)}}function g(){var e={};return y(arguments,function(t){y(t,function(t,n){e[n]=t})}),e}var v=Object.prototype.toString;e.exports={isArray:n,isArrayBuffer:r,isFormData:o,isArrayBufferView:i,isString:s,isNumber:u,isObject:f,isUndefined:a,isDate:c,isFile:p,isBlob:l,isStandardBrowserEnv:m,forEach:y,merge:g,trim:d}},function(e,t,n){(function(t){"use strict";e.exports=function(e){return new Promise(function(r,o){try{"undefined"!=typeof XMLHttpRequest||"undefined"!=typeof ActiveXObject?n(6)(r,o,e):"undefined"!=typeof t&&n(6)(r,o,e)}catch(i){o(i)}})}}).call(t,n(5))},function(e,t){function n(){f=!1,s.length?a=s.concat(a):c=-1,a.length&&r()}function r(){if(!f){var e=setTimeout(n);f=!0;for(var t=a.length;t;){for(s=a,a=[];++c<t;)s&&s[c].run();c=-1,t=a.length}s=null,f=!1,clearTimeout(e)}}function o(e,t){this.fun=e,this.array=t}function i(){}var s,u=e.exports={},a=[],f=!1,c=-1;u.nextTick=function(e){var t=new Array(arguments.length-1);if(arguments.length>1)for(var n=1;n<arguments.length;n++)t[n-1]=arguments[n];a.push(new o(e,t)),1!==a.length||f||setTimeout(r,0)},o.prototype.run=function(){this.fun.apply(null,this.array)},u.title="browser",u.browser=!0,u.env={},u.argv=[],u.version="",u.versions={},u.on=i,u.addListener=i,u.once=i,u.off=i,u.removeListener=i,u.removeAllListeners=i,u.emit=i,u.binding=function(e){throw new Error("process.binding is not supported")},u.cwd=function(){return"/"},u.chdir=function(e){throw new Error("process.chdir is not supported")},u.umask=function(){return 0}},function(e,t,n){"use strict";var r=n(2),o=n(3),i=n(7),s=n(8),u=n(9);e.exports=function(e,t,a){var f=u(a.data,a.headers,a.transformRequest),c=o.merge(r.headers.common,r.headers[a.method]||{},a.headers||{});o.isFormData(f)&&delete c["Content-Type"];var p=new(XMLHttpRequest||ActiveXObject)("Microsoft.XMLHTTP");if(p.open(a.method.toUpperCase(),i(a.url,a.params),!0),p.timeout=a.timeout,p.onreadystatechange=function(){if(p&&4===p.readyState){var n=s(p.getAllResponseHeaders()),r=-1!==["text",""].indexOf(a.responseType||"")?p.responseText:p.response,o={data:u(r,n,a.transformResponse),status:p.status,statusText:p.statusText,headers:n,config:a};(p.status>=200&&p.status<300?e:t)(o),p=null}},o.isStandardBrowserEnv()){var l=n(10),d=n(11),h=d(a.url)?l.read(a.xsrfCookieName||r.xsrfCookieName):void 0;h&&(c[a.xsrfHeaderName||r.xsrfHeaderName]=h)}if(o.forEach(c,function(e,t){f||"content-type"!==t.toLowerCase()?p.setRequestHeader(t,e):delete c[t]}),a.withCredentials&&(p.withCredentials=!0),a.responseType)try{p.responseType=a.responseType}catch(m){if("json"!==p.responseType)throw m}o.isArrayBuffer(f)&&(f=new DataView(f)),p.send(f)}},function(e,t,n){"use strict";function r(e){return encodeURIComponent(e).replace(/%40/gi,"@").replace(/%3A/gi,":").replace(/%24/g,"$").replace(/%2C/gi,",").replace(/%20/g,"+").replace(/%5B/gi,"[").replace(/%5D/gi,"]")}var o=n(3);e.exports=function(e,t){if(!t)return e;var n=[];return o.forEach(t,function(e,t){null!==e&&"undefined"!=typeof e&&(o.isArray(e)&&(t+="[]"),o.isArray(e)||(e=[e]),o.forEach(e,function(e){o.isDate(e)?e=e.toISOString():o.isObject(e)&&(e=JSON.stringify(e)),n.push(r(t)+"="+r(e))}))}),n.length>0&&(e+=(-1===e.indexOf("?")?"?":"&")+n.join("&")),e}},function(e,t,n){"use strict";var r=n(3);e.exports=function(e){var t,n,o,i={};return e?(r.forEach(e.split("\n"),function(e){o=e.indexOf(":"),t=r.trim(e.substr(0,o)).toLowerCase(),n=r.trim(e.substr(o+1)),t&&(i[t]=i[t]?i[t]+", "+n:n)}),i):i}},function(e,t,n){"use strict";var r=n(3);e.exports=function(e,t,n){return r.forEach(n,function(n){e=n(e,t)}),e}},function(e,t,n){"use strict";var r=n(3);e.exports={write:function(e,t,n,o,i,s){var u=[];u.push(e+"="+encodeURIComponent(t)),r.isNumber(n)&&u.push("expires="+new Date(n).toGMTString()),r.isString(o)&&u.push("path="+o),r.isString(i)&&u.push("domain="+i),s===!0&&u.push("secure"),document.cookie=u.join("; ")},read:function(e){var t=document.cookie.match(new RegExp("(^|;\\s*)("+e+")=([^;]*)"));return t?decodeURIComponent(t[3]):null},remove:function(e){this.write(e,"",Date.now()-864e5)}}},function(e,t,n){"use strict";function r(e){var t=e;return s&&(u.setAttribute("href",t),t=u.href),u.setAttribute("href",t),{href:u.href,protocol:u.protocol?u.protocol.replace(/:$/,""):"",host:u.host,search:u.search?u.search.replace(/^\?/,""):"",hash:u.hash?u.hash.replace(/^#/,""):"",hostname:u.hostname,port:u.port,pathname:"/"===u.pathname.charAt(0)?u.pathname:"/"+u.pathname}}var o,i=n(3),s=/(msie|trident)/i.test(navigator.userAgent),u=document.createElement("a");o=r(window.location.href),e.exports=function(e){var t=i.isString(e)?r(e):e;return t.protocol===o.protocol&&t.host===o.host}},function(e,t,n){"use strict";function r(){this.handlers=[]}var o=n(3);r.prototype.use=function(e,t){return this.handlers.push({fulfilled:e,rejected:t}),this.handlers.length-1},r.prototype.eject=function(e){this.handlers[e]&&(this.handlers[e]=null)},r.prototype.forEach=function(e){o.forEach(this.handlers,function(t){null!==t&&e(t)})},e.exports=r},function(e,t){"use strict";e.exports=function(e){return function(t){return e.apply(null,t)}}}])});`
