// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package docs holds no implementation. It exists so that the documentation
// this repository publishes — the README, CONTRIBUTING.md and the three specs —
// has somewhere to be checked from.
//
// Every one of those documents points at the others, and at directories and
// sections inside them, by relative path. A link is the one part of a document
// that goes wrong silently: renaming a heading or moving a file breaks every
// reference to it without touching a line of the file that carries the
// reference, and nothing about a broken link fails a build or reads oddly on the
// page it is written on. So the rule that every relative link resolves from the
// file it is written in is an assertion here rather than a thing somebody
// remembers to re-audit.
package docs
