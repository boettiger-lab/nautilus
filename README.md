# images

Config files for a JupyterHub serving students in [ESPM-157](https://espm-157.carlboettiger.info) building applications using LLMs with Langchain and Streamlit on the [National Research Platform](https://nationalresearchplatform.org/), part of [The National Artificial Intelligence Research Resource (NAIRR) Pilot](https://nairrpilot.org/)

***in development***


- `hub_up.sh` Runs a kubernetes helm chart deploy using `values.yaml` and `secrets.yaml` configuration on Nautilus.  See [official docs](https://ucsd-prp.gitlab.io/userdocs/jupyter/jupyterhub-service/) for details.
- `Dockerfile` + `jupyter-ai.yml` specify our custom image dependencies. Image built automatically by GitLab CI [see docs](https://ucsd-prp.gitlab.io/userdocs/development/gitlab/) on the [NRP GitLab instance](https://gitlab.nrp-nautilus.io/cboettig/images/) and GitHub Actions on the GitHub fork. 


