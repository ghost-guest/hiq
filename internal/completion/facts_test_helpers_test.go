package completion

import "github.com/zzycxz/fairpeer/internal/evidence"

func Build(_ any, ledger *evidence.Ledger) Report { return BuildFacts(ledger, "", nil) }
