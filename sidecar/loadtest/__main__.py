"""CLI: python -m loadtest smoke | soak [--minutes N] [--live] | embed [--seconds N]

`embed` is OPT-IN and is not part of `smoke`: it loads the real 1.2 GB text encoder into a ~1.9 GB
child and takes minutes. See loadtest/embed.py."""
import argparse
import sys


def main():
    ap = argparse.ArgumentParser(prog="loadtest")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("smoke")
    sk = sub.add_parser("soak")
    sk.add_argument("--minutes", type=float, default=30.0)
    sk.add_argument("--live", action="store_true")
    em = sub.add_parser("embed")
    em.add_argument("--seconds", type=float, default=None,
                    help="sustained-encode window (default KELD_LOADTEST_EMBED_SECONDS, 180)")
    em.add_argument("--quick", action="store_true",
                    help="cap the sustained window at 45s (a shape check, not a leak measurement)")
    args = ap.parse_args()

    if args.cmd == "smoke":
        from loadtest.smoke import run
        sys.exit(run())
    if args.cmd == "soak":
        from loadtest.soak import run
        sys.exit(run(minutes=args.minutes, live=args.live))
    if args.cmd == "embed":
        # Imported HERE, not at module scope: `smoke` must not pay a line of this arm's cost.
        from loadtest.embed import run
        sys.exit(run(seconds=args.seconds, quick=args.quick))


if __name__ == "__main__":
    main()
