from assist.anchoring import QuotedFix, locate_fixes, utf16_len


def apply(text: str, fixes) -> str:
    """Applies located fixes to text (positions are UTF-16)."""
    encoded = text.encode("utf-16-le")
    for fix in sorted(fixes, key=lambda f: f.start, reverse=True):
        encoded = (
            encoded[: fix.start * 2] + fix.replacement.encode("utf-16-le") + encoded[fix.end * 2 :]
        )
    return encoded.decode("utf-16-le")


def test_narrows_to_changed_word():
    text = "yesterday i said i would fix it"
    [a, b] = locate_fixes(
        text,
        [QuotedFix("yesterday i said", "yesterday I said"), QuotedFix("said i would", "said I would")],
    )
    assert (a.start, a.end, a.original, a.replacement) == (10, 11, "i", "I")
    assert (b.start, b.end, b.original, b.replacement) == (17, 18, "i", "I")
    assert apply(text, [a, b]) == "yesterday I said I would fix it"


def test_whole_word_is_replaced_not_part_of_it():
    [fix] = locate_fixes("I recieve it", [QuotedFix("recieve it", "receive it")])
    assert (fix.original, fix.replacement) == ("recieve", "receive")


def test_inserted_punctuation():
    text = "Hello world how are you"
    [fix] = locate_fixes(text, [QuotedFix("world how", "world, how")])
    assert (fix.original, fix.replacement) == ("world", "world,")
    assert apply(text, [fix]) == "Hello world, how are you"


def test_text_not_found_is_dropped():
    assert locate_fixes("all good here", [QuotedFix("not in text", "Not in text")]) == []


def test_no_change_is_dropped():
    assert locate_fixes("same", [QuotedFix("same", "same")]) == []


def test_overlapping_fixes_keep_first():
    fixes = locate_fixes(
        "their going home",
        [QuotedFix("their going", "they're going"), QuotedFix("their going", "there going")],
    )
    assert len(fixes) == 1 and fixes[0].replacement == "they're"


def test_nearby_fixes_that_do_not_overlap_are_both_kept():
    text = "their going home"
    fixes = locate_fixes(
        text,
        [QuotedFix("their going", "they're going"), QuotedFix("going home", "going to home")],
    )
    assert apply(text, fixes) == "they're going to home"


def test_positions_are_utf16_after_emoji():
    text = "🎉 party tonite"  # the emoji is 2 UTF-16 units
    [fix] = locate_fixes(text, [QuotedFix("party tonite", "party tonight")])
    assert fix.start == utf16_len("🎉 party ") == 9
    assert apply(text, [fix]) == "🎉 party tonight"


def test_hindi_text():
    text = "मैं घर जाता हूँ and i am happy"
    [fix] = locate_fixes(text, [QuotedFix("and i am", "and I am")])
    assert apply(text, [fix]) == "मैं घर जाता हूँ and I am happy"