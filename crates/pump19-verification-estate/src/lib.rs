#![forbid(unsafe_code)]

//! Integration-test host for Pump-19's run-scoped verification estate.
//!
//! This crate intentionally exposes no product API. It gives Cargo a stable
//! package target for journey-level tests that span crates and commissioned
//! behaviours, without folding those tests into a unit crate's own conformance
//! suite.
