# Chapter 3: Binary Search Trees

## Prerequisites

- Basic tree terminology (root, leaf, depth, height)
- Recursion and recursive data structures
- Comparison-based sorting concepts

## Key definitions

**Binary search tree (BST):** A binary tree where for every node, all keys
in the left subtree are less than the node's key, and all keys in the right
subtree are greater.

**Balanced BST:** A BST where the height is O(log n), ensuring efficient
operations. Examples: AVL trees, red-black trees.

**In-order traversal:** Visiting nodes in left-root-right order, which
produces keys in sorted order for a BST.

## Learning objectives

- Implement insert, search, and delete operations on a BST
- Analyze time complexity for balanced and unbalanced cases
- Understand when to use a BST versus a hash table

## Exercises

1. Insert the keys [5, 3, 7, 1, 4, 6, 8] into an empty BST and draw the result
2. Delete the root node and show the resulting tree
3. What is the worst-case height of a BST with n nodes?
