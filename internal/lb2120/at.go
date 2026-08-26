// Package lb2120 talks to the Netgear LB2120's AT-command telnet interface
// (port 5510) and its web JSON API.
package lb2120

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ATStatus is a snapshot of modem state read via AT commands.
type ATStatus struct {
	CFUN         int // 0 = radio off (the stuck state), 1 = full functionality
	CSQRSSI      int // 0-31 signal index, 99 = unknown
	CSQBER       int
	CREGStat     int // 0=not registered/not searching, 1=home, 2=searching, 3=denied, 5=roaming
	CEREGStat    int // same encoding, for the LTE (EPS) domain
	CGATT        int // 0/1 attached to packet service
	COPSOperator string
	COPSAct      int // access technology, 7 = E-UTRAN/LTE
	CBCStatus    int
	CBCLevel     int
}

var (
	reCFUN  = regexp.MustCompile(`\+CFUN:\s*(\d+)`)
	reCSQ   = regexp.MustCompile(`\+CSQ:\s*(\d+),(\d+)`)
	reCREG  = regexp.MustCompile(`\+CREG:\s*(\d+),(\d+)`)
	reCEREG = regexp.MustCompile(`\+CEREG:\s*(\d+),(\d+)`)
	reCGATT = regexp.MustCompile(`\+CGATT:\s*(\d+)`)
	reCOPS  = regexp.MustCompile(`\+COPS:\s*(\d+)(?:,(\d+),"([^"]*)",(\d+))?`)
	reCBC   = regexp.MustCompile(`\+CBC:\s*(\d+),(\d+)`)
)

// sendAT writes a single AT command and reads until it sees a terminal
// OK/ERROR line or the overall deadline expires.
func sendAT(conn net.Conn, cmd string, overall time.Duration) (string, error) {
	if err := conn.SetDeadline(time.Now().Add(overall)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "", fmt.Errorf("write %s: %w", cmd, err)
	}

	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			s := buf.String()
			if strings.Contains(s, "OK\r\n") || strings.Contains(s, "ERROR") {
				return s, nil
			}
		}
		if err != nil {
			// Timeout or EOF: return whatever was accumulated.
			return buf.String(), nil
		}
	}
}

// QueryStatus opens a fresh connection to the AT interface and reads the
// registration/signal state relevant to diagnosing and monitoring the modem.
func QueryStatus(ctx context.Context, addr string, timeout time.Duration) (ATStatus, error) {
	var st ATStatus

	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return st, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	perCmd := timeout
	if perCmd > 5*time.Second {
		perCmd = 5 * time.Second
	}

	if resp, err := sendAT(conn, "AT+CFUN?", perCmd); err == nil {
		if m := reCFUN.FindStringSubmatch(resp); m != nil {
			st.CFUN, _ = strconv.Atoi(m[1])
		} else {
			return st, fmt.Errorf("AT+CFUN? unparseable response: %q", resp)
		}
	} else {
		return st, err
	}

	if resp, err := sendAT(conn, "AT+CSQ", perCmd); err == nil {
		if m := reCSQ.FindStringSubmatch(resp); m != nil {
			st.CSQRSSI, _ = strconv.Atoi(m[1])
			st.CSQBER, _ = strconv.Atoi(m[2])
		}
	}

	if resp, err := sendAT(conn, "AT+CREG?", perCmd); err == nil {
		if m := reCREG.FindStringSubmatch(resp); m != nil {
			st.CREGStat, _ = strconv.Atoi(m[2])
		}
	}

	if resp, err := sendAT(conn, "AT+CEREG?", perCmd); err == nil {
		if m := reCEREG.FindStringSubmatch(resp); m != nil {
			st.CEREGStat, _ = strconv.Atoi(m[2])
		}
	}

	if resp, err := sendAT(conn, "AT+CGATT?", perCmd); err == nil {
		if m := reCGATT.FindStringSubmatch(resp); m != nil {
			st.CGATT, _ = strconv.Atoi(m[1])
		}
	}

	if resp, err := sendAT(conn, "AT+COPS?", perCmd); err == nil {
		if m := reCOPS.FindStringSubmatch(resp); m != nil {
			st.COPSOperator = strings.TrimSpace(m[3])
			if m[4] != "" {
				st.COPSAct, _ = strconv.Atoi(m[4])
			}
		}
	}

	if resp, err := sendAT(conn, "AT+CBC", perCmd); err == nil {
		if m := reCBC.FindStringSubmatch(resp); m != nil {
			st.CBCStatus, _ = strconv.Atoi(m[1])
			st.CBCLevel, _ = strconv.Atoi(m[2])
		}
	}

	return st, nil
}

// Recover sends AT+CFUN=1 (radio on, no reset) which is the command that
// reliably clears the "stuck in low power mode" state; AT+CFUN=1,1 (full
// reset) was observed NOT to reliably clear it.
func Recover(ctx context.Context, addr string, timeout time.Duration) error {
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	resp, err := sendAT(conn, "AT+CFUN=1", timeout)
	if err != nil {
		return err
	}
	if !strings.Contains(resp, "OK\r\n") {
		return fmt.Errorf("AT+CFUN=1 did not return OK: %q", resp)
	}
	return nil
}
