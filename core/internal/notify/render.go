package notify

import (
	"bytes"
	"embed"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"text/template"
	"time"
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

// pages are where each kind of notification sends people in the console.
// pageFor narrows it to the exact request or campaign when it can.
var pages = map[Kind]string{
	ApprovalNeeded:   "/approvals",
	RequestExpiring:  "/approvals",
	RequestDecided:   "/requests",
	ReviewAssigned:   "/reviews",
	CampaignDueSoon:  "/reviews",
	SoDViolation:     "/governance/separation-of-duties",
	BreakGlassUsed:   "/vault?tab=break-glass",
	BreakGlassDue:    "/vault?tab=break-glass",
	TokenCreatorLeft: "/settings/api-tokens",
	ThreatDetected:   "/security/threats",
	OwnerLeft:        "/governance/non-human-identities",
	SettingChange:    "/settings/general",
}

// pageFor is the console page a notification of kind, with data, links to:
// the request or campaign it is about when the data names one, so nobody has
// to look for it in a list.
func pageFor(kind Kind, data map[string]any) string {
	page := pages[kind]
	param := map[Kind][2]string{
		ApprovalNeeded:  {"request", "request_id"},
		RequestExpiring: {"request", "request_id"},
		RequestDecided:  {"request", "request_id"},
		ReviewAssigned:  {"campaign", "campaign_id"},
		CampaignDueSoon: {"campaign", "campaign_id"},
	}[kind]
	if id, ok := data[param[1]].(string); ok && id != "" {
		page += "?" + url.Values{param[0]: {id}}.Encode()
	}
	return page
}

var templates = func() map[Kind]*template.Template {
	funcs := template.FuncMap{
		// date formats an RFC 3339 time from the data, in UTC.
		"date": func(v any) string {
			s, _ := v.(string)
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return s
			}
			return t.UTC().Format("Mon 2 Jan 2006 15:04 UTC")
		},
		// plural picks the word for a count from the data.
		"plural": func(v any, one, many string) string {
			if n, ok := v.(float64); ok && n == 1 {
				return one
			}
			if n, ok := v.(int); ok && n == 1 {
				return one
			}
			return many
		},
	}
	out := map[Kind]*template.Template{}
	for _, k := range Kinds {
		out[k.Kind] = template.Must(template.New(string(k.Kind)).Funcs(funcs).
			Option("missingkey=zero").ParseFS(templateFiles, "templates/"+string(k.Kind)+".tmpl"))
	}
	return out
}()

// view is what a template sees.
type view struct {
	// Name is the recipient's display name, and RecipientID their identity.
	Name        string
	RecipientID string
	// URL is the console page to act on the notification.
	URL  string
	Data map[string]any
	// SettingsURL is where the recipient turns notifications off.
	SettingsURL string
}

// render returns the subject and plain text body of a notification.
func render(kind Kind, v view) (subject, body string, err error) {
	t, ok := templates[kind]
	if !ok {
		return "", "", fmt.Errorf("no template for %q", kind)
	}
	// Requests queued before they had kinds asked for access.
	if _, ok := v.Data["kind"]; !ok && (kind == ApprovalNeeded || kind == RequestExpiring || kind == RequestDecided) {
		v.Data = maps.Clone(v.Data)
		if v.Data == nil {
			v.Data = map[string]any{}
		}
		v.Data["kind"] = "access"
	}
	var s, b bytes.Buffer
	if err := t.ExecuteTemplate(&s, "subject", v); err != nil {
		return "", "", err
	}
	if err := t.ExecuteTemplate(&b, "body", v); err != nil {
		return "", "", err
	}
	// A subject is one line, whatever the data holds.
	subject = strings.Join(strings.Fields(s.String()), " ")
	body = strings.TrimLeft(b.String(), "\n") +
		"\n--\nGrantra sends this because of your role in access requests, reviews, separation of duties, the vault or security.\n" +
		"Choose which emails you get: " + v.SettingsURL + "\n"
	return subject, body, nil
}
