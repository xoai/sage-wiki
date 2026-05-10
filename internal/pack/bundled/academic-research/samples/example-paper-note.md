# Attention Is All You Need

Vaswani et al., 2017

## Summary

Introduces the Transformer architecture, replacing recurrence and convolution
with self-attention for sequence transduction. Achieves state-of-the-art BLEU
scores on English-to-German and English-to-French translation.

## Methodology

- Architecture: encoder-decoder with multi-head self-attention
- Training: WMT 2014 datasets, 8 NVIDIA P100 GPUs, 3.5 days
- Evaluation: BLEU scores on translation benchmarks

## Key findings

- Self-attention alone is sufficient for sequence modeling
- Multi-head attention enables learning different representation subspaces
- Positional encoding preserves sequence order without recurrence

## Related work

- Builds on: Bahdanau attention (2014), sequence-to-sequence models
- Contradicts: assumption that recurrence is necessary for sequence modeling
