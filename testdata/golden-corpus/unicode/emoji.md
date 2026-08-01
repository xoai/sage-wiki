# Emoji in Technical Content 🚀

Modern documentation includes emoji: 🎯 for targets, ⚠️ for warnings,
✅ for completions, and ❌ for failures. The search index must tokenize
emoji-adjacent words correctly — "rocket🚀launch" should not merge
into a single unfindable token. Zero-width joiners 👨‍👩‍👧‍👦 and
variation selectors ✈️ are edge cases for UTF-8 handling.
