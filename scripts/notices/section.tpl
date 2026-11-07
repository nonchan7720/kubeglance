{{ range . }}
## {{ .Name }}

License: {{ .LicenseName }}

```
{{ .LicenseText }}
```
{{ end -}}
