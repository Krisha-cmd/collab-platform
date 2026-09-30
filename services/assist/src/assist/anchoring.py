"""Turns fixes described by text ("replace 'teh' with 'the'") into exact ranges.

Language models are unreliable at counting character positions, so they are
never asked to. Instead each fix quotes the original text, and this module
finds it in the input, narrows it to the words that actually change, and
drops anything that cannot be found.
"""

from dataclasses import dataclass


def utf16_len(s: str) -> int:
    """Length of s in UTF-16 code units: the unit all document positions use."""
    return len(s.encode("utf-16-le")) // 2


@dataclass(frozen=True)
class QuotedFix:
    original: str
    replacement: str


@dataclass(frozen=True)
class LocatedFix:
    start: int  # UTF-16 offset into the searched text
    end: int  # UTF-16 offset, exclusive
    original: str  # exactly the text in [start, end)
    replacement: str
    source_index: int  # which input fix this came from


def locate_fixes(text: str, fixes: list[QuotedFix]) -> list[LocatedFix]:
    """Finds each fix in text. Returns them sorted, non-overlapping."""
    located: list[LocatedFix] = []
    cursor = 0  # fixes are expected in reading order; search onward first

    for i, fix in enumerate(fixes):
        original, replacement = fix.original, fix.replacement
        if not original or original == replacement:
            continue

        index = text.find(original, cursor)
        if index == -1:
            index = text.find(original)  # out of order: try from the start
        if index == -1:
            continue  # quoted text is not in the document: drop the fix
        cursor = index + len(original)

        prefix, suffix = _unchanged_edges(original, replacement)
        start = index + prefix
        end = index + len(original) - suffix
        located.append(
            LocatedFix(
                start=utf16_len(text[:start]),
                end=utf16_len(text[:end]),
                original=text[start:end],
                replacement=replacement[prefix : len(replacement) - suffix],
                source_index=i,
            )
        )

    located.sort(key=lambda f: (f.start, f.end))
    result: list[LocatedFix] = []
    for fix in located:
        if result and fix.start < result[-1].end:
            continue  # overlaps the previous fix: keep only the first
        result.append(fix)
    return result


def _unchanged_edges(original: str, replacement: str) -> tuple[int, int]:
    """Lengths of the unchanged start and end, widened to whole words.

    "yesterday i said" -> "yesterday I said" gives the range of "i" alone.
    "teh cat" -> "the cat" gives "teh", not just "eh", so the UI shows words.
    """
    limit = min(len(original), len(replacement))

    prefix = 0
    while prefix < limit and original[prefix] == replacement[prefix]:
        prefix += 1
    while prefix > 0 and not original[prefix - 1].isspace():
        prefix -= 1  # back up to the start of the word

    suffix = 0
    while (
        suffix < limit - prefix
        and original[len(original) - 1 - suffix] == replacement[len(replacement) - 1 - suffix]
    ):
        suffix += 1
    while suffix > 0 and not original[len(original) - suffix].isspace():
        suffix -= 1  # the kept end must begin at a word boundary

    return prefix, suffix