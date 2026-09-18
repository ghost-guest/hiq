package completion

import "github.com/zzycxz/hiq/internal/evidence"

func Build(_ any, ledger *evidence.Ledger) Report { return BuildFacts(ledger, "", nil) }
