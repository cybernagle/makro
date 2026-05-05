class Fingersaver < Formula
  desc "AI coding agent orchestrator with split-pane TUI"
  homepage "https://github.com/cybernagle/fingersaver"
  url "https://github.com/cybernagle/fingersaver/archive/refs/tags/v0.4.1.tar.gz"
  sha256 "e9bd7563128951ba4233cb9bd1d5b2700768f8502c6ffc9f8eb2f4baf3bdd262"
  version "0.4.1"

  depends_on "go" => :build
  depends_on "tmux"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/fingersaver --version")
  end
end
