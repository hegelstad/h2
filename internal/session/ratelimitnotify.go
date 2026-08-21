package session

import (
	"fmt"
	"log"
	"net"
	"time"

	"h2/internal/config"
	"h2/internal/session/message"
	"h2/internal/socketdir"
)

// isNewRateLimit reports whether a usage-limit hit (resetting at resetsAt) is a
// newly-observed limit versus a previously recorded one. It is used to notify
// the user only once per limit instead of on every monitor poll while the same
// limit persists. A nil prev (no record), an expired prev, or a different reset
// time all count as new.
func isNewRateLimit(prev *config.RateLimitInfo, resetsAt time.Time) bool {
	if prev == nil {
		return true
	}
	// A previously recorded limit that has already expired is stale; a fresh
	// hit after it is new.
	if !prev.ResetsAt.IsZero() && time.Now().After(prev.ResetsAt) {
		return true
	}
	// Same reset minute => same limit, already notified.
	return !prev.ResetsAt.Truncate(time.Minute).Equal(resetsAt.Truncate(time.Minute))
}

// buildRateLimitNotice formats the user-facing alert sent over bridges when an
// agent hits a Claude/Codex usage limit. Plain text (no rich/HTML markup) so it
// renders on every bridge and client.
func buildRateLimitNotice(agentName, profile string, resetsAt time.Time) string {
	if profile == "" {
		profile = "default"
	}
	var reset string
	if resetsAt.IsZero() {
		reset = "Tilbakestilling: ukjent."
	} else {
		reset = "Tilbakestilles " + resetsAt.Local().Format("15:04 02.01.")
	}
	return fmt.Sprintf("⚠️ Claude-bruksgrense nådd for agent «%s» (profil «%s»). %s Bytt profil med: h2 rotate %s",
		agentName, profile, reset, agentName)
}

// notifyBridges delivers body to every running bridge, best-effort. It dials
// each bridge socket the same way `h2 send <bridge>` does. Failures are logged
// and never block the caller; bridges run in separate processes, so this works
// even when the calling agent is itself rate limited. from is left empty for a
// system notice so the bridge does not prefix an agent tag.
func notifyBridges(from, body string) {
	bridges, err := socketdir.ListByType(socketdir.TypeBridge)
	if err != nil {
		log.Printf("rate-limit notify: list bridges: %v", err)
		return
	}
	for _, b := range bridges {
		conn, err := net.DialTimeout("unix", b.Path, 2*time.Second)
		if err != nil {
			log.Printf("rate-limit notify: dial bridge %q: %v", b.Name, err)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := message.SendRequest(conn, &message.Request{Type: "send", From: from, Body: body}); err != nil {
			log.Printf("rate-limit notify: send to bridge %q: %v", b.Name, err)
			conn.Close()
			continue
		}
		if _, err := message.ReadResponse(conn); err != nil {
			log.Printf("rate-limit notify: response from bridge %q: %v", b.Name, err)
		}
		conn.Close()
	}
}
