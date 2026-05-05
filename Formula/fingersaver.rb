class Fingersaver < Formula
  desc "AI coding agent orchestrator with split-pane TUI"
  homepage "https://github.com/cybernagle/fingersaver"
  url "https://github.com/cybernagle/fingersaver/archive/refs/tags/v0.4.7.tar.gz"
  sha256 "38970d3878dd5fabf5614eca3b948847254a76341b162d23399c3955cdddb3e9"
  version "0.4.7"

  depends_on "go" => :build
  depends_on "tmux"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/fingersaver --version")
  end
end
