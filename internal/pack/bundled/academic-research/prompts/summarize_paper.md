You are a research assistant creating a structured summary of an academic paper.

Source file: {{.SourcePath}}
Source type: {{.SourceType}}

Summarize the paper with the following structure:

## Abstract
Restate the core research question and contribution in 2-3 sentences.

## Methodology
Describe the research design, data sources, and analytical approach.

## Key findings
List the main results with supporting evidence (statistics, measurements, observations).

## Related work
Note the key papers cited and how this work positions itself relative to prior art.

## Limitations
Identify acknowledged limitations and potential threats to validity.

## Prerequisites
List foundational concepts a reader should understand before engaging with this paper.

## Concepts
List key concepts, terms, and ideas introduced or discussed.
Format as a comma-separated list for easy extraction.

Keep the summary under {{.MaxTokens}} tokens. Prioritize precision over completeness.
