# Contributing to Switchboard

First off, thanks for being interested! This project is an experiment in building a B2BUA from scratch, and extra hands (or eyes) are always welcome.

## Before You Start

Please understand what you're getting into:

### This Is Unstable

- The architecture is still being decided
- APIs will change without notice
- Your PR might become irrelevant if a subsystem gets rewritten
- There are almost no tests (yet)

### This Is a Learning Project

- Response times will vary (this is a side project)
- "Perfect" is not the goal; "working and understandable" is
- If something seems wrong, it probably is - please point it out

### Unstable Development

This project uses some AI Assisted coding for rapid prototyping. That means:
- Code is generated and modified across multiple files simultaneously
- There may be inconsistencies between components
- Documentation might lag behind code

## How to Contribute

### Reporting Issues

Found a bug? Something doesn't work? Open an issue with:

1. What you were trying to do
2. What happened instead
3. Relevant logs (sanitize any sensitive info)
4. Your environment (OS, Go version, etc.)

### Suggesting Features

Have an idea? Open an issue to discuss before implementing. This helps:
- Avoid duplicate work
- Check if it fits the project direction
- Identify potential conflicts with planned changes

### Submitting Code

1. **Fork the repository**

2. **Create a branch**
   ```bash
   git checkout -b feature/your-feature-name
   ```

3. **Make your changes**
   - Follow existing code style
   - Add comments where the "why" isn't obvious
   - Don't introduce new dependencies without discussion

4. **Test your changes**
   ```bash
   go build ./...
   go test ./... # (when we have tests)
   ```

5. **Submit a PR**
   - Describe what the change does
   - Reference any related issues
   - Be prepared for feedback/iteration

### Code Style

- Standard Go formatting (`go fmt`)
- Meaningful variable names
- Comments explain "why", code explains "what"
- Keep functions focused and small
- Error messages should be helpful

### Commit Messages

No strict format, but please:
- Use present tense ("Add feature" not "Added feature")
- First line is a summary (50 chars or less ideal)
- More detail in the body if needed

## What Would Help Most

If you're looking for ways to contribute, here are areas that need attention:

### Testing
- Unit tests for existing packages
- Integration tests for call flows
- Test fixtures and helpers

### Documentation
- Code comments for complex functions
- Package-level documentation
- Examples and tutorials

### Code Review
- Read through existing code
- Point out issues, inconsistencies, or improvements
- SIP RFC compliance checks

### Specific Areas
- Error handling improvements
- Logging consistency
- Configuration validation
- SIP edge cases

## Questions?

- Open an issue for project-related questions
- Check existing issues first (someone might have asked already)

## Code of Conduct

Be kind. This is a learning project - treat others as you'd want to be treated when you're learning something new.

---

*Thanks for considering contributing to Switchboard!*
