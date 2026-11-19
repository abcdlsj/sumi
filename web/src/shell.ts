const KEY = "sumi.shell";

/** Moves `?shell=desktop` into sessionStorage and marks <html> so CSS can reserve the title bar strip. */
export function captureShellFromURL(): void {
  const url = new URL(window.location.href);
  if (url.searchParams.get("shell") === "desktop") {
    sessionStorage.setItem(KEY, "desktop");
    url.searchParams.delete("shell");
    window.history.replaceState(null, "", url.pathname + url.search + url.hash);
  }
  document.documentElement.classList.toggle("desktop", sessionStorage.getItem(KEY) === "desktop");
}
