This run was on the branch https://github.com/ethereum/go-ethereum/pull/19391, where a hash storage was introduced. 

This should make the blockhash operation into an O(1) very cheap operation (sidechains would be O(length of sidechain))
