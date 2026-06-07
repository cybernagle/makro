class Fingersaver < Formula
  desc "AI coding agent orchestrator with split-pane TUI"
  homepage "https://github.com/cybernagle/fingersaver"
  url "https://github.com/cybernagle/makro/archive/refs/tags/v0.4.19.tar.gz"
  sha256 "11e745feb195ab22eb1f3af8b81170309167637f6538372f389c8ccf2c285438"
  version "0.4.19"

  depends_on "go" => :build
  depends_on "tmux"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/fingersaver --version")
  end
end
