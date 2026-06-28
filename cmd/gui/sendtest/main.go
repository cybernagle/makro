package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const tmuxSocket = "sendtest99"
const session = "receiver"

func parseTmuxArgs(cmd string) []string {
	var args []string
	var cur strings.Builder
	inS, inD := false, false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case inS:
			if ch == '\'' {
				inS = false
			} else {
				cur.WriteByte(ch)
			}
		case inD:
			if ch == '"' {
				inD = false
			} else if ch == '\\' && i+1 < len(cmd) {
				i++
				cur.WriteByte(cmd[i])
			} else {
				cur.WriteByte(ch)
			}
		default:
			switch ch {
			case '\'':
				inS = true
			case '"':
				inD = true
			case ' ', '\t':
				if cur.Len() > 0 {
					args = append(args, cur.String())
					cur.Reset()
				}
			default:
				cur.WriteByte(ch)
			}
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func quoteArg(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\") {
		return s
	}
	e := strings.ReplaceAll(s, `\`, `\\`)
	e = strings.ReplaceAll(e, `"`, `\"`)
	return `"` + e + `"`
}

func tmuxExec(args ...string) string {
	full := append([]string{"-L", tmuxSocket}, args...)
	out, _ := exec.Command("/opt/homebrew/bin/tmux", full...).CombinedOutput()
	return string(out)
}

func capturePane() string {
	return tmuxExec("capture-pane", "-t", session, "-p", "-S", "-50")
}

// idle: pane 里没有 thinking/Metamorphosing 等活跃标志，且有 ❯ 提示符
func isIdle() bool {
	pane := capturePane()
	busy := strings.Contains(pane, "thinking") || strings.Contains(pane, "Metamorphosing") ||
		strings.Contains(pane, "Baking") || strings.Contains(pane, "Cogitat") ||
		strings.Contains(pane, "Working") || strings.Contains(pane, "✻") || strings.Contains(pane, "✢")
	hasPrompt := strings.Contains(pane, "❯")
	return hasPrompt && !busy
}

func waitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isIdle() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// 当前代码: bracketed paste + "; Enter" 单次 tmux 调用
func sendOneStep(text string) error {
	body := text
	if len(text) > 10 {
		body = fmt.Sprintf("\033[200~%s\033[201~", text)
	}
	cmd := fmt.Sprintf("send-keys -t %s -l %s ; send-keys -t %s Enter",
		quoteArg(session), quoteArg(body), quoteArg(session))
	_, err := exec.Command("/opt/homebrew/bin/tmux", append([]string{"-L", tmuxSocket}, parseTmuxArgs(cmd)...)...).CombinedOutput()
	return err
}

// 旧代码: 两次独立调用
func sendTwoStep(text string) error {
	body := text
	if len(text) <= 10 {
		c1 := fmt.Sprintf("send-keys -t %s -l %s", quoteArg(session), quoteArg(body))
		if out, err := exec.Command("/opt/homebrew/bin/tmux", append([]string{"-L", tmuxSocket}, parseTmuxArgs(c1)...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w (%s)", err, out)
		}
		c2 := fmt.Sprintf("send-keys -t %s Enter", quoteArg(session))
		_, err := exec.Command("/opt/homebrew/bin/tmux", append([]string{"-L", tmuxSocket}, parseTmuxArgs(c2)...)...).CombinedOutput()
		return err
	}
	payload := fmt.Sprintf("\033[200~%s\033[201~\r", body)
	c := fmt.Sprintf("send-keys -t %s -l %s", quoteArg(session), quoteArg(payload))
	_, err := exec.Command("/opt/homebrew/bin/tmux", append([]string{"-L", tmuxSocket}, parseTmuxArgs(c)...)...).CombinedOutput()
	return err
}

// 发送后轮询: marker 出现 = 提交成功; 超时 = Enter 丢失
func sendAndCheck(name string, i int, sendFn func(string) error) bool {
	marker := fmt.Sprintf("MK%s%03d", name, i)
	msg := fmt.Sprintf("reply with exactly: %s", marker)
	if err := sendFn(msg); err != nil {
		fmt.Printf("  #%d SEND_ERR: %v\n", i, err)
		return false
	}
	// 轮询最多 25 秒看 marker 是否出现 (说明 claude 收到并回复了)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(capturePane(), marker) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func runTest(name string, sendFn func(string) error, n int) {
	fmt.Printf("\n=== %s: %d 次 ===\n", name, n)
	failures := 0
	for i := 0; i < n; i++ {
		// 等 claude idle
		if !waitIdle(30 * time.Second) {
			fmt.Printf("  #%d SKIP: claude 没回到 idle\n", i)
			failures++
			continue
		}
		ok := sendAndCheck(name, i, sendFn)
		if ok {
			fmt.Printf("  #%d ok ✓\n", i)
		} else {
			fmt.Printf("  #%d FAIL: marker 没出现 (Enter 丢失?) ✗\n", i)
			failures++
		}
	}
	fmt.Printf(">>> %s 结果: %d/%d 失败 (%.0f%%)\n", name, failures, n, float64(failures)*100/float64(n))
}

func main() {
	fmt.Println("=== send_to_session 间歇性 Enter 丢失测试 ===")
	fmt.Printf("进程: %s", tmuxExec("list-panes", "-t", session, "-F", "#{pane_current_command}"))

	// 先确认 idle
	fmt.Print("等待 claude idle...")
	if waitIdle(30 * time.Second) {
		fmt.Println(" OK")
	} else {
		fmt.Println(" 超时, 强制开始")
	}

	// 先发一条确认链路通
	fmt.Print("\n链路自检: 发一条 PROBE...")
	if sendAndCheck("PROBE", 0, sendOneStep) {
		fmt.Println(" 通了 ✓")
	} else {
		fmt.Println(" 不通! 检查环境")
		return
	}
	// 等 PROBE 那条处理完
	waitIdle(30 * time.Second)

	runTest("oneStep(当前代码)", sendOneStep, 15)
	runTest("twoStep(旧代码)", sendTwoStep, 15)
}
