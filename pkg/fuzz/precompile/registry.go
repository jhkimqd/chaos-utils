// Package precompile implements EVM precompile invariant testing.
// It verifies that known precompile addresses return the correct output for
// fixed test vectors, and that invalid (undeployed) addresses return empty.
//
// The registry is split into three sections:
//  1. Standard EVM precompiles (0x01–0x09): active in all post-Byzantium chains.
//  2. Polygon PoS-specific precompiles: add new hard-fork entries here.
//  3. Invalid addresses: must return "0x" (no code deployed, not a precompile).
package precompile

import (
	"fmt"
	"math/rand"
)

// Entry describes one precompile test case.
type Entry struct {
	// Address is the lowercase checksumless hex address, e.g. "0x0000...0002".
	Address string
	// Name is a human-readable label used in log messages.
	Name string
	// Input is the hex-encoded eth_call data field (the precompile input).
	Input string
	// Expected is the hex-encoded expected return value.
	// Only consulted when Check == "exact".
	Expected string
	// Check is the validation mode:
	//   "exact"     – return value must equal Expected byte-for-byte
	//   "non_empty" – return value must be non-empty (not "" or "0x")
	//   "empty"     – return value must be "" or "0x" (address has no code)
	Check string
	// Critical marks high-severity entries: failure is highlighted in the report.
	Critical bool
}

// KnownPrecompiles lists standard EVM precompiles (EIP-150 / Byzantium / Istanbul)
// that must be active on every post-Byzantium chain, including Polygon PoS / Bor.
//
// Test vectors are sourced from the Ethereum test suite and EIP specifications.
var KnownPrecompiles = []Entry{
	// ── 0x01  ecrecover (secp256k1 signature recovery) ────────────────────
	// Vector from the Ethereum yellow paper Appendix F / EIP-2.
	{
		Address: "0x0000000000000000000000000000000000000001",
		Name:    "ecrecover",
		// Input: hash(32) || v(32) || r(32) || s(32) = 128 bytes
		Input: "0x" +
			"456e9aea5e197a1f1af7a3e85a3212fa4049a3ba34c2289b4c860fc0b0c64ef3" + // msg hash
			"000000000000000000000000000000000000000000000000000000000000001c" + // v = 28
			"9242685bf161793cc25603c231bc2f568eb630ea16aa137d2664ac8038825608" + // r
			"4f8ae3bd7535248d0bd448298cc2e2071e56992d0774dc340c368ae950852ada", // s
		// Expected: zero-padded recovered address (32 bytes)
		Expected: "0x0000000000000000000000007156526fbd7a3c72969b54f64e42c10fbb768c8a",
		Check:    "exact",
		Critical: true,
	},

	// ── 0x02  SHA-256 hash ─────────────────────────────────────────────────
	// sha256("a") = ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb
	{
		Address:  "0x0000000000000000000000000000000000000002",
		Name:     "sha256",
		Input:    "0x61", // ASCII "a"
		Expected: "0xca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb",
		Check:    "exact",
		Critical: true,
	},

	// ── 0x03  RIPEMD-160 hash ──────────────────────────────────────────────
	// ripemd160("a") = 0bdc9d2d256b3ee9daae347be6f4dc835a467ffe
	// Output is right-padded to 32 bytes (20 bytes preceded by 12 zero bytes).
	{
		Address:  "0x0000000000000000000000000000000000000003",
		Name:     "ripemd160",
		Input:    "0x61", // ASCII "a"
		Expected: "0x0000000000000000000000000bdc9d2d256b3ee9daae347be6f4dc835a467ffe",
		Check:    "exact",
		Critical: true,
	},

	// ── 0x04  identity (data copy) ─────────────────────────────────────────
	// Output = input verbatim.
	{
		Address:  "0x0000000000000000000000000000000000000004",
		Name:     "identity",
		Input:    "0xdeadbeef",
		Expected: "0xdeadbeef",
		Check:    "exact",
		Critical: true,
	},

	// ── 0x05  modexp (EIP-198) ─────────────────────────────────────────────
	// Computes: base^exp mod modulus
	// Here: 2^3 mod 5 = 3
	// Encoding: Bbase(32) || Bexp(32) || Bmod(32) || base(Bbase) || exp(Bexp) || mod(Bmod)
	//   Bbase=1, Bexp=1, Bmod=1, base=0x02, exp=0x03, mod=0x05
	{
		Address: "0x0000000000000000000000000000000000000005",
		Name:    "modexp",
		Input: "0x" +
			"0000000000000000000000000000000000000000000000000000000000000001" + // Bbase = 1 byte
			"0000000000000000000000000000000000000000000000000000000000000001" + // Bexp  = 1 byte
			"0000000000000000000000000000000000000000000000000000000000000001" + // Bmod  = 1 byte
			"02" + // base = 2
			"03" + // exp  = 3
			"05", // mod  = 5
		Expected: "0x03", // 2^3 mod 5 = 8 mod 5 = 3
		Check:    "exact",
		Critical: false,
	},

	// ── 0x06  BN256Add (EIP-196) ───────────────────────────────────────────
	// G1 + identity-point = G1
	// G1 generator = (1, 2); identity = (0, 0)
	// Input: x1(32) || y1(32) || x2(32) || y2(32)
	{
		Address: "0x0000000000000000000000000000000000000006",
		Name:    "bn256add",
		Input: "0x" +
			"0000000000000000000000000000000000000000000000000000000000000001" + // x1
			"0000000000000000000000000000000000000000000000000000000000000002" + // y1
			"0000000000000000000000000000000000000000000000000000000000000000" + // x2 (identity)
			"0000000000000000000000000000000000000000000000000000000000000000", // y2 (identity)
		// G1 + identity = G1 = (1, 2)
		Expected: "0x" +
			"0000000000000000000000000000000000000000000000000000000000000001" +
			"0000000000000000000000000000000000000000000000000000000000000002",
		Check:    "exact",
		Critical: false,
	},

	// ── 0x07  BN256ScalarMul (EIP-196) ────────────────────────────────────
	// 1 * G1 = G1
	// Input: x(32) || y(32) || scalar(32)
	{
		Address: "0x0000000000000000000000000000000000000007",
		Name:    "bn256scalarmul",
		Input: "0x" +
			"0000000000000000000000000000000000000000000000000000000000000001" + // x
			"0000000000000000000000000000000000000000000000000000000000000002" + // y
			"0000000000000000000000000000000000000000000000000000000000000001", // scalar = 1
		// 1 * G1 = G1 = (1, 2)
		Expected: "0x" +
			"0000000000000000000000000000000000000000000000000000000000000001" +
			"0000000000000000000000000000000000000000000000000000000000000002",
		Check:    "exact",
		Critical: false,
	},

	// ── 0x08  BN256Pairing (EIP-197) ──────────────────────────────────────
	// Empty input = trivial pairing result = true = 1
	{
		Address:  "0x0000000000000000000000000000000000000008",
		Name:     "bn256pairing",
		Input:    "0x",
		Expected: "0x0000000000000000000000000000000000000000000000000000000000000001",
		Check:    "exact",
		Critical: false,
	},

	// ── 0x09  BLAKE2F (EIP-152) ────────────────────────────────────────────
	// Input: rounds(4) || h(64) || m(128) || t(16) || f(1) = 213 bytes
	// All-zero input with 0 rounds returns the 64-byte initial state unchanged (all zeros).
	// We use "non_empty" since 64 zero-bytes is a valid non-trivial return value.
	{
		Address: "0x0000000000000000000000000000000000000009",
		Name:    "blake2f",
		// 213 zero bytes: rounds=0 (4B) || h=0 (64B) || m=0 (128B) || t=0 (16B) || f=0 (1B)
		Input: "0x" +
			"00000000" + // rounds = 0
			"0000000000000000000000000000000000000000000000000000000000000000" + // h[0]
			"0000000000000000000000000000000000000000000000000000000000000000" + // h[1]
			"0000000000000000000000000000000000000000000000000000000000000000" + // m[0..3]
			"0000000000000000000000000000000000000000000000000000000000000000" +
			"0000000000000000000000000000000000000000000000000000000000000000" +
			"0000000000000000000000000000000000000000000000000000000000000000" +
			"00000000000000000000000000000000" + // t (16B)
			"00", // f = false
		Check:    "non_empty",
		Critical: false,
	},
}

// PolygonPrecompiles lists Polygon PoS-specific system precompiles.
// Add new hard-fork precompile entries here as they are deployed.
//
// To add a new precompile:
//   1. Set Address to the activation address (e.g., "0x000...1000").
//   2. Provide a known Input + Expected pair, or use Check: "non_empty" if
//      the exact return value is not yet known.
//   3. Set Critical: true if the chain cannot function without it.
var PolygonPrecompiles = []Entry{
	// Example template — replace with actual Polygon-specific precompiles:
	// {
	//     Address:  "0x0000000000000000000000000000000000001000",
	//     Name:     "bor-validator-set",
	//     Input:    "0x",
	//     Check:    "non_empty",
	//     Critical: true,
	// },
}

// InvalidAddresses lists addresses that are NOT EVM precompiles on any standard
// chain and must return "0x" (empty bytes) when called.
// These are used as negative-test cases: if any of them start returning non-empty
// data, an unknown precompile or contract was silently deployed at that address.
var InvalidAddresses = []Entry{
	{Address: "0x000000000000000000000000000000000000000a", Name: "undefined-0x0a", Input: "0x", Check: "empty"},
	{Address: "0x000000000000000000000000000000000000000b", Name: "undefined-0x0b", Input: "0x", Check: "empty"},
	{Address: "0x000000000000000000000000000000000000000c", Name: "undefined-0x0c", Input: "0x", Check: "empty"},
	{Address: "0x000000000000000000000000000000000000000d", Name: "undefined-0x0d", Input: "0x", Check: "empty"},
	{Address: "0x000000000000000000000000000000000000000e", Name: "undefined-0x0e", Input: "0x", Check: "empty"},
	{Address: "0x000000000000000000000000000000000000000f", Name: "undefined-0x0f", Input: "0x", Check: "empty"},
	// A higher-range address: very unlikely to have code in a fresh devnet.
	{Address: "0x0000000000000000000000000000000000000064", Name: "undefined-0x64", Input: "0x", Check: "empty"},
}

// All returns KnownPrecompiles + PolygonPrecompiles combined.
// This is the set of "must work" precompile entries used by the fuzzer.
func All() []Entry {
	combined := make([]Entry, 0, len(KnownPrecompiles)+len(PolygonPrecompiles))
	combined = append(combined, KnownPrecompiles...)
	combined = append(combined, PolygonPrecompiles...)
	return combined
}

// SampleRandomInvalidAddress returns a random hex address in the range [0x0a, 0xffff]
// formatted as a 20-byte EVM address. Addresses in this range are not standard EVM
// precompiles and should return "0x" when called via eth_call.
// The caller supplies an *rand.Rand so sampling is deterministic and reproducible.
func SampleRandomInvalidAddress(rng *rand.Rand) string {
	const lo, hi = 0x0a, 0xffff
	addrNum := int64(lo) + rng.Int63n(int64(hi-lo+1))
	return fmt.Sprintf("0x%040x", addrNum)
}
