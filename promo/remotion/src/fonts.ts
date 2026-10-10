import { loadFont as loadVt323 } from "@remotion/google-fonts/VT323";
import { loadFont as loadPressStart2P } from "@remotion/google-fonts/PressStart2P";
import { loadFont as loadIbmPlexMono } from "@remotion/google-fonts/IBMPlexMono";

// Loaded at module scope so every composition blocks rendering until the
// fonts are ready — a half-loaded frame would silently fall back to a system
// mono and look wrong in the final render only.

loadVt323("normal", { subsets: ["latin"] });
loadPressStart2P("normal", { subsets: ["latin"], weights: ["400"] });
loadIbmPlexMono("normal", {
  subsets: ["latin"],
  weights: ["400", "500", "600"],
});
