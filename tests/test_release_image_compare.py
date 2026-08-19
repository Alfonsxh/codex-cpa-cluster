import unittest
from unittest import mock

from scripts import release_image_compare


class ReleaseImageCompareTests(unittest.TestCase):
    def image(self, *, base="base", command=("openresty",), architecture="amd64"):
        return {
            "Architecture": architecture,
            "Os": "linux",
            "Config": {
                "Cmd": list(command),
                "Entrypoint": None,
                "Env": ["PATH=/usr/local/bin"],
                "ExposedPorts": None,
                "Healthcheck": None,
                "OnBuild": None,
                "Shell": None,
                "StopSignal": None,
                "User": "",
                "Volumes": None,
                "WorkingDir": "",
            },
            "RootFS": {"Layers": [base, "copy-layer", "runtime-layer"]},
        }

    def test_edge_runtime_equivalence_allows_only_last_two_layer_metadata_to_differ(self):
        current = self.image()
        candidate = self.image()
        candidate["RootFS"]["Layers"][-2:] = ["new-copy", "new-runtime"]

        self.assertTrue(
            release_image_compare.edge_runtime_equivalent(
                current, candidate, "same-files", "same-files"
            )
        )

    def test_edge_runtime_equivalence_rejects_base_config_or_file_changes(self):
        current = self.image()
        cases = []
        different_base = self.image(base="other-base")
        cases.append((different_base, "same-files"))
        different_config = self.image(command=("different",))
        cases.append((different_config, "same-files"))
        different_architecture = self.image(architecture="arm64")
        cases.append((different_architecture, "same-files"))
        cases.append((self.image(), "different-files"))

        for candidate, fingerprint in cases:
            with self.subTest(candidate=candidate, fingerprint=fingerprint):
                self.assertFalse(
                    release_image_compare.edge_runtime_equivalent(
                        current, candidate, "same-files", fingerprint
                    )
                )

    @mock.patch("scripts.release_image_compare.edge_runtime_fingerprint")
    @mock.patch("scripts.release_image_compare.inspect_image")
    def test_compare_edge_images_inspects_both_images(self, inspect_image, fingerprint):
        inspect_image.side_effect = (self.image(), self.image())
        fingerprint.side_effect = ("same", "same")

        self.assertTrue(release_image_compare.compare_edge_images("current", "candidate"))
        self.assertEqual(
            inspect_image.call_args_list,
            [mock.call("current"), mock.call("candidate")],
        )
        self.assertEqual(
            fingerprint.call_args_list,
            [mock.call("current"), mock.call("candidate")],
        )


if __name__ == "__main__":
    unittest.main()
