This run was on the branch https://github.com/ethereum/go-ethereum/pull/19291, where a blockcontext was introduced. 
It contained some measures that should make blockhash significantly faster, at least in cases where it was used maliciously. 
However, it would still maintain roughly the same low performance if execution only happens rarely. 
