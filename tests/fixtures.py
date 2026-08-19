import sys
from pathlib import Path


ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
from control_plane_store import ControlPlaneStore  # noqa: E402


TEST_ACCOUNT_IDS = ["alpha", "beta", "gamma", "delta"]
TEST_INITIAL_PORTAL_PASSWORD = "fixture-initial-password"


def seed_control_plane(root, domains=None, key_prefix="cpa_"):
    """Seed neutral authoritative SQLite fixtures for ControlPlane tests."""
    root = Path(root)
    accounts = [
        {
            "id": account,
            "email": "{}@accounts.example.com".format(account),
            "port": 18319 + index,
            "created_at": 1_700_000_000 + index,
            "group_enabled": True,
            "default_group": index == 0,
        }
        for index, account in enumerate(TEST_ACCOUNT_IDS)
    ]
    store = ControlPlaneStore(root)
    store.write_accounts(accounts)
    store.write_settings(
        {
            "identity.allowed_email_domains": list(domains or ["example.com"]),
            "identity.key_prefix": key_prefix,
            "branding.public_base_url": "http://cpa.example.com",
        }
    )
    store.write_secret("portal_initial_password", TEST_INITIAL_PORTAL_PASSWORD)
    return accounts
