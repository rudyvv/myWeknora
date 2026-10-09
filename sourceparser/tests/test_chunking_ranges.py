"""Budget and coverage properties independent of grammar loading."""
import random
import unittest
from chunking import attach_whitespace, pack_boundaries, text_ranges


class ChunkingRangeTests(unittest.TestCase):
    def assert_ranges(self, raw, maximum, ranges):
        cursor = 0
        for start, end in ranges:
            self.assertEqual(start, cursor)
            self.assertGreater(end, start)
            self.assertLessEqual(end - start, maximum)
            raw[start:end].decode('utf-8')
            cursor = end
        self.assertEqual(cursor, len(raw))

    def test_seeded_unicode_lines_preserve_every_byte_at_all_budgets(self):
        generator = random.Random(731)
        for _ in range(150):
            raw = ''.join(generator.choice(('😀', '中文', 'line', ' ', '\r\n', ';', '\\'))
                          for _ in range(generator.randrange(1, 1000))).encode()
            for maximum in (64, 128, 512, 4096):
                self.assert_ranges(raw, maximum, text_ranges(raw, maximum))

    def test_empty_and_all_whitespace_files_preserve_original(self):
        for raw in (b'', b' \r\n' * 1000):
            self.assert_ranges(raw, 64, text_ranges(raw, 64))

    def test_long_leaf_keeps_a_final_suffix_with_payload(self):
        raw = b'a' * 512 + b');'
        ranges, _ = pack_boundaries(raw, 512, [])
        self.assert_ranges(raw, 512, ranges)
        self.assertTrue(raw[ranges[-1][0]:].startswith(b'a'))

    def test_docker_continuations_are_not_preferred_cuts(self):
        raw = b'RUN echo one \\\n  && echo two\nENV NAME=value\n'
        ranges = text_ranges(raw, 64)
        self.assertEqual(ranges, [(0, len(raw))])

    def test_whitespace_at_full_unit_keeps_budget(self):
        raw = b'a' * 64 + b'\n' + b'b' * 64
        ranges = attach_whitespace(raw, [(0, 64), (64, 65), (65, 129)], 64)
        self.assert_ranges(raw, 64, ranges)


if __name__ == '__main__':
    unittest.main()
