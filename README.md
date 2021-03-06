# GCR Cleaner

This project is a fork of https://github.com/sethvargo/gcr-cleaner, so massive thanks to them for making a great repo!

GCR Cleaner deletes tagged and untagged images in Google Container Registry. This can help
reduce costs and keep your container images list in order.

This version of GCR Cleaner is designed to be run as a cronjob in GKE Kubernetes environments
and is intended for cleaning GCR repos with children. It uses an exceptions file as well as
finds tags currently in use across multiple GKE clusters and filters those out of consideration
for deletion.

The deletion itself works by first querying all of the clusters in a provided kube config for all pod, cronjob, and job
resources and checking which tags are being used by all of them. It then goes through all child repos of the provided
base repo, keeps the last 5 tags (or however many you want) *based on tag name in ascending order (will delete the lowest in
ascending string order)* for each then keeps additional tags if they are specified in the exceptions file. Everything else will
be deleted, including untagged manifests. If the exceptions file specifies entire child repos those child repos will only have
untagged manifests deleted and nothing else.

## Dry Run

Important to note is the dry run option for this program. If you want to see what would potentially happen in a standard run without
risking your existing manifests, change the command in your CronJob resource to `/bin/gcrcleaner -dry`. This will output which manifests
would be deleted for each child repo, how many would be kept, and how much space each repo would still use after cleaning.

## Setup

1. Create a service account that has the `roles/storage.admin` (Storage Admin) role for the GCR bucket as well as
   the `roles/container.viewer` (Kubernetes Engine Viewer) role for all of the projects that have GKE clusters you want to
   filter based on

2. Create a JSON key for the new service account

3. Create a kube config file using the JSON key you generated - this guide might be helpful https://ahmet.im/blog/authenticating-to-gke-without-gcloud/

4. Create a docker config file with the same JSON key by generating the auth with this command
   ```SH
   docker login -u _json_key --password-stdin https://gcr.io < account.json
   ```

5. Create a JSON file for child repo or tag exceptions that you never want to be deleted under any circumstances.
   It should be in the following format:
   ```JSON
   {
    "repo": [
      "child-repo"
    ],
    "tag": [
      "another-child-repo:2019-12-31",
      "another-child-repo:2019-11-25"
    ],
    "globalTag": [
      "latest"
    ]
   }
   ```

5. Deploy the GCR Cleaner as a cronjob in your Kubernetes cluster. Proper functionality requires the following:
   - The JSON key file, the kube config file, the docker config file, and the exceptions json file must all be available on the pod.
     This can be achieved by mounting them as secrets, or mounting a volume that contains them
   - These environment variables must be defined:<br/>
      `KUBECONFIG`: The path to your kube config file<br/>
      `DOCKER_CONFIG`: The path to your docker config file<br/>
      `GOOGLE_APPLICATION_CREDENTIALS`: The path to your service account JSON key<br/>
      `GCR_BASE_REPO`: The name of your GCR repo in the format `gcr.io/{project}`<br/>
   - These environment variables are optional:<br/>
      `CLEANER_EXCEPTION_FILE`: The path to the exceptions JSON file (default is `/config/exceptions.json`)<br/>
      `CLEANER_KEEP_AMOUNT`: The minimum amount of tags in each child repo that must be kept (default is 5)<br/>
  The default command for this image is `/bin/gcrcleaner`. To use the dry run, change it to `/bin/gcrcleaner -dry`.

## License

This library is licensed under Apache 2.0. Full license text is available in
[LICENSE](https://github.com/farmersedgeinc/gcr-cleaner/tree/master/LICENSE).
