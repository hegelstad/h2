package opencode

import _ "embed"

//go:embed assets/h2-bridge.ts
var pluginSource []byte

//go:embed assets/opencode.json.tmpl
var configJSONTemplate []byte
