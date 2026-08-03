package s3900

// Administration accounts, session secret, and persistence.
//
// Saving on 1.7.0 is not a simple "write": "copy running-config
// startup-config" replies "Startup configuration file name:" and waits for a
// NAME (captured in the lab AND in the 2026-08-02 deployment). The answer is
// always explicit — accepting a default could overwrite
// Factory_Default_Config.cfg, the file the soft reset relies on.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

var (
	reSavePrompt  = regexp.MustCompile(`(?i)startup\s+configuration\s+file\s+name\s*:`)
	reSaveOK      = regexp.MustCompile(`(?i)flash\s+programming\s+completed|\bsuccess\b|\[OK\]`)
	reSaveOrAsk   = regexp.MustCompile(`(?i)startup\s+configuration\s+file\s+name\s*:|flash\s+programming\s+completed|\bsuccess\b|\[OK\]|continue.*[yn]|\(y/n\)`)
	reRunUsername = regexp.MustCompile(`(?im)^\s*username\s+(\S+)\s`)
)

// save copies the current configuration into the startup configuration.
func (c *Client) save(ctx context.Context) error {
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}
	var werr = c.sendAndExpectSave(ctx, "copy running-config startup-config")
	return werr
}

func (c *Client) sendAndExpectSave(ctx context.Context, cmd string) error {
	var err = c.cli.sendLine(cmd)
	if err != nil {
		return err
	}
	var out string
	out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOrAsk)
	if err != nil {
		return fmt.Errorf("no reaction to the save command: %v", err)
	}
	if reSavePrompt.MatchString(out) {
		// The answer to the prompt is a LINE: the startup file name.
		err = c.cli.sendLine(StartupConfigFile)
		if err != nil {
			return err
		}
		out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOK)
		if err != nil {
			return fmt.Errorf("flash write not confirmed after sending %q: %v",
				StartupConfigFile, err)
		}
	}
	if !reSaveOK.MatchString(out) {
		// y/n from another firmware in the family: confirm, then wait.
		if strings.Contains(strings.ToLower(out), "y/n") || strings.Contains(strings.ToLower(out), "continue") {
			err = c.cli.sendLine("y")
			if err != nil {
				return err
			}
			out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOK)
			if err != nil {
				return fmt.Errorf("flash write not confirmed after y: %v", err)
			}
		}
	}
	if !reSaveOK.MatchString(out) {
		return fmt.Errorf("save finished without a Success marker")
	}
	// Regain control on a clean prompt.
	var _, perr = c.cli.run(ctx, "", cmdTimeout)
	_ = perr
	return nil
}

// Persist — driver.Persister: the platform distinguishes applying from
// persisting (Commit = apply_then_save).
func (c *Client) Persist(ctx context.Context) error {
	var err = c.save(ctx)
	if err != nil {
		c.step(false, "startup-config save failed", firstLine(err.Error()))
		return err
	}
	c.step(true, "configuration persisted", "copy running-config startup-config → "+StartupConfigFile)
	return nil
}

// Accounts lists the administration accounts present (running-config).
func (c *Client) Accounts(ctx context.Context) ([]string, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return nil, err
	}
	var seen = map[string]bool{}
	var out []string
	var m []string
	for _, m = range reRunUsername.FindAllStringSubmatch(runOut, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out, nil
}

// EnsureAccount guarantees that a named account exists and carries this
// secret. Idempotent: "username" creates or modifies depending on what
// already exists — the CLI doesn't distinguish the two verbs. DOES NOT
// PERSIST — sequencing a rename is the caller's responsibility (cf.
// driver.AccountManager).
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.TrimSpace(user) == "" || secret == "" {
		return fmt.Errorf("account name and secret are both required")
	}
	// Forms captured in the lab: privilege first, password next (two commands).
	var err = c.runConfigBlock(ctx, []string{
		fmt.Sprintf("username %s privilege 15", user),
		fmt.Sprintf("username %s password 0 %s", user, secret),
		// enable shares the same secret: it is the only door from user EXEC to
		// privileged mode, and meshcraft stores only one secret per account.
		fmt.Sprintf("enable password 0 %s", secret),
	})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not written", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q ensured on device", user),
		"privilege 15; enable password aligned on the same secret")
	return nil
}

// DeleteAccount removes an administration account.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.EqualFold(user, c.User) {
		return fmt.Errorf("refusing to delete %q: it is the account of the current session", user)
	}
	// DEDUCED form (never captured): the CLI's refusal surfaces as-is.
	var err = c.runConfigBlock(ctx, []string{"no username " + user})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not deleted", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted", user))
	return nil
}

// SetSecret changes the password OF THE ACCOUNT THAT JUST LOGGED IN, and
// PERSISTS (contract: an administration secret that doesn't survive a reboot
// puts the device back on its factory account).
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("no account has been proven on this device: there is " +
			"no password to change, and meshcraft does not guess the holder name")
	}
	_ = oldSecret // the CLI does not require the old secret: the open session is proof enough
	var err = c.EnsureAccount(ctx, c.User, newSecret)
	if err != nil {
		return err
	}
	return c.Persist(ctx)
}

// ApplyMgmtAddressing writes management addressing ON THE CURRENT VLAN —
// never a VLAN change (that would be MgmtMover, not implemented here).
//
// Forms captured: "interface vlan N" + "ip add A.B.C.D/nn",
// "ip route 0.0.0.0 0.0.0.0 GW", "dns name-server X" (lab running-config).
// The post-write re-read can fail if the address THIS session came in through
// just changed: the result then says so instead of pretending it verified.
func (c *Client) ApplyMgmtAddressing(ctx context.Context, want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out = driver.MgmtAddrResult{}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	var st, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	var vlanID = mgmtVlanOf(st.Interface)
	if vlanID <= 0 {
		return out, fmt.Errorf("the device declares no management SVI: meshcraft does " +
			"not know which interface to address (a VLAN move is a different gesture)")
	}

	var cmds []string
	switch want.Mode {
	case driver.MgmtAddrModeDHCP:
		cmds = append(cmds, fmt.Sprintf("interface vlan %d", vlanID), "ip add dhcp", "exit")
	case driver.MgmtAddrModeStatic:
		if !strings.Contains(want.Address, "/") {
			return out, fmt.Errorf("static address must carry its prefix (A.B.C.D/nn)")
		}
		cmds = append(cmds, fmt.Sprintf("interface vlan %d", vlanID),
			"ip add "+want.Address, "exit")
	default:
		return out, fmt.Errorf("unknown addressing mode %q", want.Mode)
	}
	if strings.TrimSpace(want.Gateway) != "" {
		cmds = append(cmds, "ip route 0.0.0.0 0.0.0.0 "+strings.TrimSpace(want.Gateway))
	}
	var dns string
	for _, dns = range want.DNS {
		if strings.TrimSpace(dns) != "" {
			cmds = append(cmds, "dns name-server "+strings.TrimSpace(dns))
		}
	}

	err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		return out, err
	}
	c.step(true, "management addressing written", strings.Join(cmds, " ; "))

	var after driver.MgmtAddrState
	after, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		out.Notes = append(out.Notes,
			"post-write re-read failed ("+firstLine(err.Error())+") — if the session address "+
				"just changed, this is expected: probe the NEW address to confirm")
		return out, nil
	}
	out.Observed = after
	return out, nil
}

func mgmtVlanOf(iface string) int {
	var re = regexp.MustCompile(`(?i)^vlan\s*(\d+)$`)
	var m = re.FindStringSubmatch(strings.TrimSpace(iface))
	if m == nil {
		return 0
	}
	var n = 0
	var _, err = fmt.Sscanf(m[1], "%d", &n)
	if err != nil {
		return 0
	}
	return n
}
