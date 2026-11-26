// live.go checks the running SubSlip: health, Instant Mix timings through it,
// and what its status says about clients and pre-caching.
//
// Works with: doctor.go (run state, localBase), report.go.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

func (d *run) checkLive(ctx context.Context) {
	client := &http.Client{Timeout: 60 * time.Second}
	base := d.localBase()
	resp, err := client.Get(base + "/subslip/health")
	if err != nil {
		d.add(Warn, "SubSlip service", "not answering at "+base, "Start the service, then run doctor again.")
		return
	}
	resp.Body.Close()
	d.add(Pass, "SubSlip service", "healthy at "+base, "")

	if d.auth.User != "" && d.songID != "" {
		q := d.auth.Values()
		q.Set("id", d.songID)
		q.Set("count", "25")
		q.Set("f", "json")
		for i := 1; i <= 2; i++ {
			started := time.Now()
			r, err := client.Get(base + "/rest/getSimilarSongs2?" + q.Encode())
			if err != nil {
				d.add(Fail, "Instant Mix via SubSlip", "request failed: "+err.Error(), "")
				return
			}
			r.Body.Close()
			d.add(Pass, fmt.Sprintf("Instant Mix via SubSlip #%d", i),
				fmt.Sprintf("HTTP %d, X-SubSlip=%s, %d ms", r.StatusCode, r.Header.Get("X-SubSlip"), time.Since(started).Milliseconds()), "")
		}
	}
	d.checkStatus(ctx, client, base)
}

func (d *run) checkStatus(ctx context.Context, client *http.Client, base string) {
	if d.cfg.StatusToken == "" {
		d.add(Warn, "status", "STATUS_TOKEN is not set, so the running service's status cannot be read",
			"Set STATUS_TOKEN in .env (any long random string) and restart the service.")
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/subslip/status", nil)
	req.Header.Set("Authorization", "Bearer "+d.cfg.StatusToken)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		d.add(Warn, "status", "could not read /subslip/status", "Does the service use the same .env as doctor?")
		return
	}
	defer resp.Body.Close()
	var doc struct {
		Mode   string `json:"mode"`
		Record struct {
			Clients []struct {
				Name       string `json:"name"`
				Requests   int64  `json:"requests"`
				FirstShape string `json:"firstInstantMixShape"`
			} `json:"clientsSeen"`
		} `json:"record"`
		Precache struct {
			ActiveSince map[string]time.Time `json:"activeSince"`
		} `json:"precache"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		d.add(Warn, "status", "unreadable status document", "")
		return
	}
	d.add(Pass, "status", "mode="+doc.Mode, "")
	real := 0
	for _, c := range doc.Record.Clients {
		if c.Name == ClientName {
			continue
		}
		real++
		d.add(Info, "client seen", fmt.Sprintf("%s: %d requests; first Instant Mix shape: %s", c.Name, c.Requests, c.FirstShape), "")
	}
	if real == 0 {
		d.add(Warn, "clients", "no client has sent requests through SubSlip since it started",
			"Your client is probably still pointed at the upstream. Change its server address to the client URL below.")
	}
	for user, since := range doc.Precache.ActiveSince {
		d.add(Info, "pre-cache", fmt.Sprintf("active for %s since %s", url.PathEscape(user), since.Format(time.RFC3339)), "")
	}
}
