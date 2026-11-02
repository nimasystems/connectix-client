package connectix

import (
	"context"
	"net/http"
	"regexp"
)

// slotPattern finds the {{ name }} variables of a template text.
var slotPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// Template is a message template of the company, made and approved by a
// person in the Connectix console.
type Template struct {
	// ID is what [SendMessageRequest.Template] takes.
	ID string
	// Name is the template's name in the console.
	Name string
	// Channel is "sms", "viber" or "noti".
	Channel string
	// Status is "approved", "pending", "waiting_information" or "rejected".
	// Only an approved template can be sent.
	Status string
	// Text is the message text with its {{ slot }} variables; empty when the
	// template has none.
	Text string
	// Slots are the variable names in Text, each once, in order of first
	// appearance. Every one needs a value in [SendMessageRequest.Parameters].
	Slots []string
}

type wireTemplate struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Channel string  `json:"channel"`
	Status  string  `json:"status"`
	Text    *string `json:"text"`
}

// ListTemplates returns every template of the company, whatever its channel
// and status. Pick an approved one of the channel you send on.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	var wire []wireTemplate
	if _, err := c.call(ctx, request{method: http.MethodGet, path: "templates"}, &wire); err != nil {
		return nil, err
	}
	out := make([]Template, 0, len(wire))
	for _, w := range wire {
		t := Template{ID: w.ID, Name: w.Name, Channel: w.Channel, Status: w.Status}
		if w.Text != nil {
			t.Text = *w.Text
			t.Slots = slots(t.Text)
		}
		out = append(out, t)
	}
	return out, nil
}

// slots returns the distinct {{ name }} variables of text in order of first
// appearance, nil when there are none.
func slots(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range slotPattern.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Me returns the name of the application the token belongs to. Use it to
// check a token against the host before anything else.
func (c *Client) Me(ctx context.Context) (string, error) {
	var me struct {
		Name string `json:"name"`
	}
	if _, err := c.call(ctx, request{method: http.MethodGet, path: "me"}, &me); err != nil {
		return "", err
	}
	return me.Name, nil
}
