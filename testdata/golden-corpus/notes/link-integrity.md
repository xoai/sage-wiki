# Link Integrity

Cross-references rot when concepts are renamed. The compile pipeline
strips links to concepts that do not exist on disk after the write
pass, so a renamed concept silently orphans its inbound links unless
an alias preserves the old name.
