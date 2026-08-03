package procurve

// The SSH transport, and the one thing about it that is NOT established.
//
// # What happened on the reference unit
//
// TCP 22 is open on the 2610-48 surveyed on 2026-08-02, and the banner grab came
// back EMPTY while an authentication attempt failed. Three explanations fit and
// this connector does not pretend to know which:
//
//   - SSH is not actually enabled. On this range it stays off until « crypto key
//     generate ssh » and « ip ssh » are run, and a listening socket is not proof
//     that the server behind it will negotiate;
//   - the ALGORITHMS did not overlap. A 2007-era ProCurve offers
//     diffie-hellman-group1-sha1, ssh-dss host keys and 3des-cbc — every one of
//     which a current client refuses by default. The prober that failed carries a
//     legacy list, so this is not the likeliest cause, but it is not excluded;
//   - the credential is wrong for the level. This range has TWO levels, and an
//     `operator` login lands on « > » with no ability to configure. A password
//     accepted at the wrong level is not the same as a password refused.
//
// Recording that as unknown is the point. Guessing would put a fact in the
// package that later readers would trust.
//
// # The algorithm list
//
// Identical to the Cisco IOS connector's and the discovery probe's, and it must
// stay identical: all three talk to the same fleet, and a range this old is
// exactly what that list exists for.

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"meshcraft/internal/driver"
)

const (
	sshPort     = "22"
	dialTimeout = 12 * time.Second
	cmdTimeout  = 20 * time.Second
	// showRunTimeout: this range prints its whole configuration in one go and a
	// 48-port unit takes seconds over a WAN-side tunnel.
	showRunTimeout = 45 * time.Second
	// saveTimeout: « write memory » writes flash and answers nothing meanwhile.
	saveTimeout = 60 * time.Second
)

// dialSSH opens the connection, honouring the Target's Dialer.
//
// The dialer is the proxy tunnel, and therefore the confidentiality boundary and
// site coverage: `ssh.Dial` would open its own connection and bypass all of it.
func dialSSH(ctx context.Context, tgt driver.Target, user, secret string) (*ssh.Client, error) {
	var addr = net.JoinHostPort(tgt.Host, sshPort)
	var conn net.Conn
	var err error
	if tgt.Dialer != nil {
		var dctx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		conn, err = tgt.Dialer.DialContext(dctx, "tcp", addr)
	} else {
		var d = net.Dialer{Timeout: dialTimeout}
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("TCP %s: %v", addr, firstLine(err.Error()))
	}

	var cfg = &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(secret),
			// Keyboard-interactive as well: this range prompts for the password
			// through it on some firmware revisions, and a connector offering only
			// `password` would read as a wrong secret.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				var answers = make([]string, len(questions))
				var i int
				for i = range questions {
					answers[i] = secret
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         dialTimeout,
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256", "curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp256", "ecdh-sha2-nistp384", "ecdh-sha2-nistp521",
				"diffie-hellman-group14-sha256", "diffie-hellman-group14-sha1",
				"diffie-hellman-group1-sha1", "diffie-hellman-group-exchange-sha1",
			},
			Ciphers: []string{
				"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
				"aes128-ctr", "aes192-ctr", "aes256-ctr",
				"aes128-cbc", "aes256-cbc", "3des-cbc",
			},
		},
		HostKeyAlgorithms: []string{
			"ssh-ed25519", "rsa-sha2-256", "rsa-sha2-512", "ssh-rsa",
			"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ssh-dss",
		},
	}

	var sc ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	sc, chans, reqs, err = ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sc, chans, reqs), nil
}
