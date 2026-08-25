package proxy

import (
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"routre-cli/internal/config"
)

// isLoopbackHost reports whether r.Host is a loopback name.
// net/http does not validate Host, so a malicious page can drive a
// victim browser to 127.0.0.1 with Host: attacker.com (DNS rebinding).
// Rejecting non-loopback Host is the standard mitigation (see Caddy
// GHSA-879p-475x-rqh2 and Go issue #23993).
func isLoopbackHost(r *http.Request) bool {
	h := r.Host
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

var uiTmpl = template.Must(template.New("ui").Funcs(template.FuncMap{"join": strings.Join}).Parse(uiHTML))

const uiHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>routre-cli — Local Settings</title>
<style>
:root{--bg:#0a0a0a;--fg:#fff;--ink2:rgb(255 255 255/56%);--ink3:rgb(255 255 255/40%);--acc:#ff3b30;--card:rgb(255 255 255/4%);--bd:rgb(255 255 255/11%);--hover:rgb(255 255 255/9%);--mono:ui-monospace,Menlo,Consolas,monospace;--sans:Inter,system-ui,sans-serif}
*{box-sizing:border-box}html,body{margin:0;background:var(--bg);color:var(--fg);font-family:var(--sans)}
a{color:var(--fg);text-decoration:none;border-bottom:1px solid var(--bd)}a:hover{border-color:var(--ink2)}
.wrap{max-width:960px;margin:0 auto;padding:0 20px}
.nav{position:sticky;top:0;backdrop-filter:blur(8px);background:rgb(10 10 10/80%);border-bottom:1px solid var(--bd);padding:12px 0}
.logo{font-weight:600;letter-spacing:-.02em}.logo em{font-style:normal;color:var(--acc)}
.card{background:var(--card);border:1px solid var(--bd);border-radius:14px;padding:16px}
.grid{display:grid;grid-template-columns:repeat(3,1fr);gap:12px;margin:16px 0}
@media(max-width:700px){.grid{grid-template-columns:1fr}}
table{width:100%;border-collapse:collapse;font-size:.88rem}th,td{border:1px solid var(--bd);padding:8px 10px;text-align:left}th{background:var(--card);font-weight:500}
textarea{width:100%;min-height:340px;background:var(--card);color:var(--fg);border:1px solid var(--bd);border-radius:10px;padding:12px;font:12px/1.6 var(--mono)}
.btn{display:inline-block;background:var(--fg);color:var(--bg);border:none;border-radius:999px;padding:9px 16px;font-weight:600;font-size:.88rem;cursor:pointer}
.btn:hover{background:rgb(255 255 255/88%)}.btn.ghost{background:transparent;color:var(--fg);border:1px solid var(--bd)}.btn.ghost:hover{background:var(--hover)}
.mono{font-family:var(--mono)}.mut{color:var(--ink2)}h2{font-size:1.15rem;margin:20px 0 10px;letter-spacing:-.02em}
label{font-size:.85rem;color:var(--ink2)}input{width:100%;background:var(--card);color:var(--fg);border:1px solid var(--bd);border-radius:8px;padding:8px 10px;font:13px var(--mono)}
.kv{display:grid;grid-template-columns:1fr 1fr;gap:10px}
@media(max-width:700px){.kv{grid-template-columns:1fr}}
</style>
</head><body>
<nav class="nav"><div class="wrap" style="display:flex;justify-content:space-between;align-items:center">
<div class="logo">routre<em>-cli</em> <span class="mono mut" style="font-weight:400;font-size:.8rem">local settings</span></div>
<div style="display:flex;gap:8px"><a class="btn ghost mono" href="/">API</a><a class="btn ghost mono" href="https://github.com/mariobgsp/routre-cli">GitHub</a></div>
</div></nav>
<main class="wrap" style="padding:20px 20px 40px">
<p class="mut" style="font-size:.9rem">Runs on <span class="mono">{{.Listen}}</span> · only reachable from this machine (127.0.0.1). Changes save to <span class="mono">{{.ConfigPath}}</span> and take effect immediately.</p>

<div class="grid">
<div class="card"><div class="mono mut" style="font-size:.72rem;text-transform:uppercase;letter-spacing:.06em">RTK</div><div style="font-weight:600">{{if .RTKEnabled}}enabled · {{.RTKLevel}}{{else}}disabled{{end}}</div><div class="mut mono" style="font-size:.78rem">{{.RTKMin}}–{{.RTKMax}} bytes</div></div>
<div class="card"><div class="mono mut" style="font-size:.72rem;text-transform:uppercase;letter-spacing:.06em">Cache</div><div style="font-weight:600">{{if .CacheEnabled}}enabled · {{.CacheEntries}} entries{{else}}disabled{{end}}</div><div class="mut mono" style="font-size:.78rem">TTL {{.CacheTTL}}s · {{.CacheBytes}} bytes</div></div>
<div class="card"><div class="mono mut" style="font-size:.72rem;text-transform:uppercase;letter-spacing:.06em">Uptime</div><div style="font-weight:600">{{.Uptime}}s</div><div class="mut mono" style="font-size:.78rem">{{.ProviderCount}} providers · {{.TierCount}} tiers</div></div>
</div>

<h2>Providers</h2>
<table class="mono"><tr><th>Tier</th><th>Provider</th><th>Kind</th><th>Models</th><th>Key</th></tr>
{{range .Tiers}}{{ $tier := .Name }}{{range .Providers}}<tr><td>{{$tier}}</td><td>{{.Name}}</td><td>{{.Kind}}</td><td style="max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{join .Models ", "}}</td><td>{{.APIKeyEnv}} {{if index $.KeysPresent .APIKeyEnv}}<span style="color:#4ade80">● set</span>{{else}}<span style="color:var(--acc)">○ missing</span>{{end}}</td></tr>{{end}}{{end}}
</table>
{{if not .Tiers}}<p class="mut">No providers configured yet — add one below or edit the JSON.</p>{{end}}

<h2>Set API key</h2>
<div class="card"><div class="kv"><div><label>Env var (e.g. OPENCODE_GO_API_KEY)</label><input id="envKey" placeholder="OPENCODE_GO_API_KEY"></div><div><label>Value</label><input id="envVal" type="password" placeholder="sk-..."></div></div>
<div style="margin-top:10px;display:flex;gap:8px;align-items:center"><button class="btn" onclick="setKey()">Save key</button><span id="keyMsg" class="mono mut" style="font-size:.82rem"></span></div>
<p class="mut mono" style="font-size:.78rem;margin-top:8px">Saved to <span class="mono">{{.EnvPath}}</span> (0600). Takes effect immediately.</p></div>

<h2>Configuration (JSON)</h2>
<p class="mut" style="font-size:.85rem">Edit and save — the gateway validates before writing. Invalid JSON is rejected and the previous config is kept.</p>
<textarea id="cfg" spellcheck="false">{{.ConfigJSON}}</textarea>
<div style="margin-top:10px;display:flex;gap:8px;align-items:center"><button class="btn" onclick="saveCfg()">Save config</button><span id="cfgMsg" class="mono" style="font-size:.82rem"></span></div>

<h2>Tips</h2>
<ul class="mut" style="font-size:.9rem;line-height:1.6">
<li>Point any agent at <span class="mono">http://{{.Listen}}</span> via <span class="mono">OPENAI_BASE_URL</span> / <span class="mono">ANTHROPIC_BASE_URL</span>.</li>
<li>Run <span class="mono">routre-cli check</span> to validate keys, <span class="mono">routre-cli list</span> for the token ledger.</li>
<li>RAM impact of this page: ~0 at idle, &lt;2 MiB after use — see <a href="https://github.com/mariobgsp/routre-cli#benchmarks">Benchmarks</a>.</li>
</ul>
</main>
<script>
async function saveCfg(){
  const el=document.getElementById('cfg'), msg=document.getElementById('cfgMsg');
  msg.textContent=' saving…'; msg.style.color='var(--ink2)';
  let body;
  try{ body=JSON.parse(el.value); }catch(e){ msg.textContent=' invalid JSON: '+e.message; msg.style.color='var(--acc)'; return; }
  const r=await fetch('/ui/api/save',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  const j=await r.json().catch(()=>({error:'bad response'}));
  if(r.ok){ msg.textContent=' saved ✓'; msg.style.color='#4ade80'; }else{ msg.textContent=' '+ (j.error||r.statusText); msg.style.color='var(--acc)'; }
}
async function setKey(){
  const k=document.getElementById('envKey').value.trim(), v=document.getElementById('envVal').value;
  const msg=document.getElementById('keyMsg');
  if(!k){ msg.textContent=' enter env var name'; msg.style.color='var(--acc)'; return; }
  msg.textContent=' saving…'; msg.style.color='var(--ink2)';
  const r=await fetch('/ui/api/env',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key:k,value:v})});
  const j=await r.json().catch(()=>({error:'bad response'}));
  if(r.ok){ msg.textContent=' saved ✓'; msg.style.color='#4ade80'; document.getElementById('envVal').value=''; }else{ msg.textContent=' '+ (j.error||r.statusText); msg.style.color='var(--acc)'; }
}
</script>
</body></html>
`

// uiData is the template data for the dashboard.
type uiData struct {
	Listen        string
	ConfigPath    string
	EnvPath       string
	RTKEnabled    bool
	RTKLevel      string
	RTKMin        int
	RTKMax        int
	CacheEnabled  bool
	CacheEntries  int
	CacheBytes    int64
	CacheTTL      int64
	Uptime        int
	ProviderCount int
	TierCount     int
	Tiers         []config.Tier
	KeysPresent   map[string]bool
	ConfigJSON    string
}

// UIDashboard serves the local settings page. Loopback-only.
func (h *Handlers) UIDashboard(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackHost(r) {
		http.Error(w, "forbidden: Host must be loopback", http.StatusForbidden)
		return
	}
	cfg := h.Cfg.Get()
	// Pretty JSON for the editor.
	j, _ := json.MarshalIndent(cfg, "", "  ")
	// Count providers.
	nProv := 0
	for _, t := range cfg.Tiers {
		nProv += len(t.Providers)
	}
	level := cfg.RTK.Level
	if level == "" {
		level = "standard"
	}
	keysPresent := map[string]bool{}
	for _, t := range cfg.Tiers {
		for _, p := range t.Providers {
			_, ok := h.Keys.Get(p.APIKeyEnv)
			keysPresent[p.APIKeyEnv] = ok
		}
	}
	data := uiData{
		Listen:        cfg.Listen,
		ConfigPath:    h.Cfg.Path(),
		EnvPath:       config.EnvFilePath(h.Cfg.Path()),
		RTKEnabled:    cfg.RTK.Enabled,
		RTKLevel:      level,
		RTKMin:        cfg.RTK.MinBytes,
		RTKMax:        cfg.RTK.MaxBytes,
		CacheEnabled:  cfg.Cache.Enabled,
		CacheEntries:  h.Cache.Len(),
		CacheBytes:    int64(h.Cache.SizeBytes()),
		CacheTTL:      cfg.Cache.TTLSeconds,
		Uptime:        int(time.Since(h.Start).Seconds()),
		ProviderCount: nProv,
		TierCount:     len(cfg.Tiers),
		Tiers:         cfg.Tiers,
		KeysPresent:   keysPresent,
		ConfigJSON:    string(j),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uiTmpl.Execute(w, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// UIConfig returns the current config as JSON (loopback-only, for fetch).
func (h *Handlers) UIConfig(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackHost(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, h.Cfg.Get())
}

// UISave validates and atomically saves the posted config JSON.
func (h *Handlers) UISave(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackHost(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Also reject non-loopback Origin if present (CSRF / rebinding).
	if o := r.Header.Get("Origin"); o != "" {
		// Allow only loopback origins.
		if !strings.Contains(o, "127.0.0.1") && !strings.Contains(o, "localhost") && !strings.Contains(o, "[::1]") {
			http.Error(w, "forbidden: Origin must be loopback", http.StatusForbidden)
			return
		}
	}
	body, err := readBody(r.Body, 1<<20)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var cfg config.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := h.Cfg.Save(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// Refresh keystore for any new api_key_env entries that may now be set
	// in the env file (user may have added keys via the key form first).
	for _, t := range cfg.Tiers {
		for _, p := range t.Providers {
			if v, ok := h.Keys.Get(p.APIKeyEnv); !ok || v == "" {
				// Try to load from env file / os env on demand; Keys.Refresh
				// is the canonical path, but direct os.LookupEnv covers the
				// case where the user exported it in shell.
				if ev, ok := h.Keys.Get(p.APIKeyEnv); ok && ev != "" {
					continue
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// UISetEnv sets a single env var in the env file (loopback-only).
func (h *Handlers) UISetEnv(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackHost(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		if !strings.Contains(o, "127.0.0.1") && !strings.Contains(o, "localhost") && !strings.Contains(o, "[::1]") {
			http.Error(w, "forbidden: Origin must be loopback", http.StatusForbidden)
			return
		}
	}
	body, err := readBody(r.Body, 64<<10)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "key is required"})
		return
	}
	envPath := config.EnvFilePath(h.Cfg.Path())
	if err := config.SetEnvFileValue(envPath, req.Key, req.Value); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	h.Keys.Set(req.Key, req.Value)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
