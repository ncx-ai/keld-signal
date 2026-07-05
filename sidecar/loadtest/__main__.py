"""CLI: python -m loadtest smoke | soak [--minutes N] [--live]"""
import argparse
import sys


def main():
    ap = argparse.ArgumentParser(prog="loadtest")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("smoke")
    sk = sub.add_parser("soak")
    sk.add_argument("--minutes", type=float, default=30.0)
    sk.add_argument("--live", action="store_true")
    args = ap.parse_args()

    if args.cmd == "smoke":
        from loadtest.smoke import run
        sys.exit(run())
    if args.cmd == "soak":
        from loadtest.soak import run
        sys.exit(run(minutes=args.minutes, live=args.live))


if __name__ == "__main__":
    main()
