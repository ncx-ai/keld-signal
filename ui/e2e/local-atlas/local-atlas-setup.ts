import signinSetup from "../signin-setup";
import { ATLAS_API, ATLAS_WEB } from "./atlas";

/**
 * Nothing to do (and nothing built) when the suite is not switched on. When it
 * is, the Atlas it names must answer before a single daemon starts: a suite
 * that "fails" 14 times because the stack is down explains nothing.
 */
export default async function localAtlasSetup(): Promise<void> {
  if (!ATLAS_WEB) {
    console.log("e2e-local-atlas: KELD_E2E_ATLAS_WEB is not set; every test will skip.");
    return;
  }
  for (const [what, url] of [
    ["Atlas web", `${ATLAS_WEB}/login`],
    ["Atlas api", `${ATLAS_API}/v1/enrichment-settings`],
  ]) {
    try {
      await fetch(url, { redirect: "manual" });
    } catch (err) {
      throw new Error(`e2e-local-atlas: ${what} at ${url} is not answering (${err}); start the local Atlas stack first`);
    }
  }
  await signinSetup();
}
