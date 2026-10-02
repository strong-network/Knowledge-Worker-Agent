# Example configuration repository

A small configuration repository for central configuration, to copy as the
start of your own. It has one artifact of each kind, and a connector for each
way users connect to one.

- To point workspaces at a repository, see
  [Set up central configuration](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/administer/set-up-central-configuration.html).
- For the full format, see
  [Configuration repository](https://docs.citrix.com/en-us/securspaces/knowledge-worker-agent/reference/configuration-repository.html).

## What's in it

```text
assignment.jsonc                   registers the artifacts and assigns them to projects
artifacts/
  mcp/
    microsoft-learn.json           a connector that needs no account
    atlassian.json                 a connector users sign in to in the browser
    acme-docs.json                 a connector that takes each user's own API key
  provider/onprem.json             a model provider on a private endpoint
  agents/writer.md                 an agent
  context/style.md                 guidance added to every chat
  skills/meeting-notes/SKILL.md    a skill
  vocabulary/org.md                names for dictation
```

Acme is a fictional company. Microsoft Learn and Atlassian are real services.
The Acme Docs connector and the model provider point at `example.com`
addresses, so they work only once you point them at real servers.

## Try it

1. Copy this folder to the workspace, or push it to a Git repository the
   workspace can read.
2. Set these environment variables for Knowledge Worker Agent, then restart it:

   | Variable | Value |
   | --- | --- |
   | `KWA_CONFIG_REPO_URL` | The folder's path, or the repository's Git URL |
   | `KWA_PROJECT_ID` | `proj_team` |
   | `KWA_CENTRAL_CONFIG` | `true` |

3. Check the log. At startup it shows the project and what it wrote:

   ```text
   ✓ Central configuration: project proj_team → … (mcp=3 agents=1 context=1 skills=1 provider=0 vocabulary=1)
   ```

4. Open the connectors. The three connectors are marked **Provided by IT**, and
   they're switched off.

Don't use `proj_onprem` until its provider points at a real endpoint. With
`KWA_CENTRAL_CONFIG` on, assigned providers replace the built-in ones, so the
workspace would have no model that works.

## How it fits together

**`assignment.jsonc`** is the one control file. It registers each artifact
under an ID, with its path, and assigns IDs to projects. A project gets exactly
the IDs it lists; to share an artifact, list its ID in each project. An ID
stands for one version of an artifact, so to change one, add it under a new ID.
Vocabularies are the exception, and you can edit them in place.

**Connectors** are opencode configuration fragments with an `mcp` block, plus
a `kwa` block that tells users how to connect (`sds`, its old name, still works):

| File | `kwa.auth.type` | Users see |
| --- | --- | --- |
| `microsoft-learn.json` | `none` | Nothing to do. It works once it's on. |
| `atlassian.json` | `oauth` | **Sign in**, to sign in in the browser. |
| `acme-docs.json` | `api-key` | **Add key**, to add their own key. `header` and `scheme` send it as `X-API-Key: <key>`. |

An artifact never sets `enabled`. Assigned connectors start switched off, and
each user turns on the ones they want.

**The model provider** defines a private endpoint and its models, with a cost
and a limit for each. Its `kwa` block says who provides the key (`managed`: you
do) and which models back **Default** and **Thinking**.

**The agent, context and skill** use opencode's own formats: an agent file, a
Markdown file added to every chat's instructions, and a folder with a
`SKILL.md`.

**The vocabulary** is a Markdown table that teaches dictation Acme's names.

**Never put a secret in the repository.** The provider reads its key from the
`ACME_MODELS_KEY` environment variable, with opencode's `{env:…}` substitution.
`{file:…}` reads a file instead, but opencode rejects its whole configuration
while that file is missing, which stops every chat. Each user's API key stays
in their own workspace.

Not shown here, and described in the reference: `deny`, `mcp_default_on`,
connectors of type `setup` and `local`, and providers that opencode already
knows.
